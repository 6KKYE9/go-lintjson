package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckValidJSON(t *testing.T) {
	good := []string{
		`{}`,
		`[]`,
		`{"a":1}`,
		`{"a": [1, 2, {"b": null}]}`,
		`  {"中文键": "中文值"}  `,
		`"就一个字符串"`,
		`123`,
		`true`,
		`null`,
	}
	for _, s := range good {
		if p := Check([]byte(s)); p != nil {
			t.Errorf("%q 是合法 JSON，却报了 %v", s, p)
		}
	}
}

// 行列号必须对得上编辑器里看到的位置。
func TestLineColBasic(t *testing.T) {
	src := []byte("abc\ndef\nghi")
	cases := []struct {
		off       int64
		line, col int
	}{
		{0, 1, 1},
		{2, 1, 3},
		{3, 1, 4},  // 换行符本身还在第 1 行
		{4, 2, 1},  // 换行之后
		{8, 3, 1},
		{10, 3, 3},
	}
	for _, c := range cases {
		l, col := lineCol(src, c.off)
		if l != c.line || col != c.col {
			t.Errorf("offset %d = (%d,%d)，想要 (%d,%d)", c.off, l, col, c.line, c.col)
		}
	}
}

// 列号要按字符算，不能按字节。
// 一个汉字 3 字节，按字节报列的话含中文的行会差出一大截。
func TestLineColCountsRunesNotBytes(t *testing.T) {
	src := []byte("中文abc")
	// "中文" 占 6 字节，后面的 'a' 在字节 6，但它是第 3 个字符。
	_, col := lineCol(src, 6)
	if col != 3 {
		t.Errorf("col = %d，想要 3（按字符数而不是字节数）", col)
	}
}

func TestLineColOutOfRange(t *testing.T) {
	src := []byte("abc")
	if l, c := lineCol(src, -5); l != 1 || c != 1 {
		t.Errorf("负偏移应该钳到开头，得到 (%d,%d)", l, c)
	}
	if l, _ := lineCol(src, 9999); l != 1 {
		t.Errorf("超界偏移应该钳到结尾，得到行 %d", l)
	}
}

// 错误位置必须指到出问题的那一行，不能差一行。
func TestCheckReportsRightLine(t *testing.T) {
	src := []byte(`{
  "a": 1,
  "b": 2,,
  "c": 3
}`)
	p := Check(src)
	if p == nil {
		t.Fatal("多余逗号应该报错")
	}
	if p.Line != 3 {
		t.Errorf("报在第 %d 行，实际错误在第 3 行\n%s", p.Line, p)
	}
}

func TestTrailingCommaInObject(t *testing.T) {
	p := Check([]byte("{\n  \"a\": 1,\n}"))
	if p == nil {
		t.Fatal("对象尾逗号应该报错")
	}
	if !strings.Contains(p.Msg, "逗号") {
		t.Errorf("错误信息应该提到逗号，实际: %s", p.Msg)
	}
}

func TestTrailingCommaInArray(t *testing.T) {
	p := Check([]byte("[1, 2, 3,]"))
	if p == nil {
		t.Fatal("数组尾逗号应该报错")
	}
	if !strings.Contains(p.Msg, "逗号") {
		t.Errorf("错误信息应该提到逗号，实际: %s", p.Msg)
	}
}

func TestSingleQuoteDetected(t *testing.T) {
	p := Check([]byte(`{'a': 1}`))
	if p == nil {
		t.Fatal("单引号应该报错")
	}
	if !strings.Contains(p.Msg, "单引号") && !strings.Contains(p.Msg, "双引号") {
		t.Errorf("应该提示引号问题，实际: %s", p.Msg)
	}
}

func TestPythonLiteralsDetected(t *testing.T) {
	cases := map[string]string{
		`{"a": None}`:  "None",
		`{"a": True}`:  "True",
		`{"a": False}`: "False",
	}
	for src, word := range cases {
		p := Check([]byte(src))
		if p == nil {
			t.Fatalf("%s 应该报错", src)
		}
		if !strings.Contains(p.Msg, word) {
			t.Errorf("%s 的提示应该点名 %s，实际: %s", src, word, p.Msg)
		}
	}
}

func TestCommentDetected(t *testing.T) {
	p := Check([]byte("{\n  // 这是注释\n  \"a\": 1\n}"))
	if p == nil {
		t.Fatal("JSON 不支持注释，应该报错")
	}
	if !strings.Contains(p.Msg, "注释") {
		t.Errorf("应该提示注释问题，实际: %s", p.Msg)
	}
}

func TestMissingCommaDetected(t *testing.T) {
	p := Check([]byte("{\n  \"a\": 1\n  \"b\": 2\n}"))
	if p == nil {
		t.Fatal("少逗号应该报错")
	}
	if !strings.Contains(p.Msg, "逗号") {
		t.Errorf("应该提示少逗号，实际: %s", p.Msg)
	}
}

func TestUnclosedDetected(t *testing.T) {
	p := Check([]byte(`{"a": 1`))
	if p == nil {
		t.Fatal("没闭合应该报错")
	}
	if !strings.Contains(p.Msg, "不完整") && !strings.Contains(p.Msg, "闭合") {
		t.Errorf("应该提示不完整，实际: %s", p.Msg)
	}
}

func TestSnippetShowsOffendingLine(t *testing.T) {
	src := []byte("{\n  \"a\": 1,\n  \"b\": 2,,\n}")
	p := Check(src)
	if p == nil {
		t.Fatal("应该报错")
	}
	if !strings.Contains(p.Snippet, `"b"`) {
		t.Errorf("片段应该是出错的那一行，得到 %q", p.Snippet)
	}
	if !strings.HasSuffix(p.Caret, "^") {
		t.Errorf("应该有 ^ 标记，得到 %q", p.Caret)
	}
}

// tab 缩进的行，^ 也得对齐。
// 用空格填充的话，tab 显示成 4 或 8 格宽，^ 就跑偏了。
func TestCaretPreservesTabs(t *testing.T) {
	src := []byte("{\n\t\"a\": 1,,\n}")
	p := Check(src)
	if p == nil {
		t.Fatal("应该报错")
	}
	if !strings.HasPrefix(p.Caret, "\t") {
		t.Errorf("caret 应该用 tab 对齐，得到 %q", p.Caret)
	}
}

// 中文占两列宽，^ 的缩进要跟着算宽度。
func TestCaretAccountsForWideChars(t *testing.T) {
	text, caret := snippet([]byte(`{"中文": x}`), 1, 8)
	if text == "" {
		t.Fatal("应该取到内容")
	}
	// 前 7 个字符里有 2 个汉字，宽度应该是 5 + 2*2 = 9
	want := 9
	got := len(strings.TrimSuffix(caret, "^"))
	if got != want {
		t.Errorf("caret 缩进宽度 = %d，想要 %d（汉字算两列）", got, want)
	}
}

// encoding/json 遇到重复键是静默覆盖的，一个字都不说。
// 配置文件里这个坑很阴：改了前面那个不生效，怎么调都不对。
func TestDuplicateKeysDetected(t *testing.T) {
	src := []byte(`{
  "port": 8080,
  "host": "a",
  "port": 9090
}`)
	// 先确认标准库确实不报错，这是本功能存在的前提
	var v any
	if err := json.Unmarshal(src, &v); err != nil {
		t.Fatal("前提不成立：标准库居然报错了")
	}

	dups := DuplicateKeys(src)
	if len(dups) != 1 {
		t.Fatalf("应该找到 1 个重复键，找到 %d 个: %v", len(dups), dups)
	}
	if !strings.Contains(dups[0].Msg, "port") {
		t.Errorf("应该点名 port，实际: %s", dups[0].Msg)
	}
	if dups[0].Line != 4 {
		t.Errorf("重复的那个 port 在第 4 行，报的是第 %d 行", dups[0].Line)
	}
}

func TestDuplicateKeysNested(t *testing.T) {
	src := []byte(`{
  "a": {"x": 1, "x": 2},
  "b": 3
}`)
	dups := DuplicateKeys(src)
	if len(dups) != 1 {
		t.Fatalf("嵌套对象里的重复键也要查出来，找到 %d 个", len(dups))
	}
}

// 不同层级的同名键是完全正常的，不能误报。
func TestDuplicateKeysDifferentLevelsAreFine(t *testing.T) {
	src := []byte(`{
  "name": "外层",
  "child": {"name": "内层"}
}`)
	if dups := DuplicateKeys(src); len(dups) != 0 {
		t.Errorf("不同层级的同名键是合法的，不该报: %v", dups)
	}
}

// 数组里每个对象各自独立，同名键不算重复。
func TestDuplicateKeysInArrayElementsAreFine(t *testing.T) {
	src := []byte(`[{"id": 1}, {"id": 2}, {"id": 3}]`)
	if dups := DuplicateKeys(src); len(dups) != 0 {
		t.Errorf("数组里各元素的同名键是合法的，不该报: %v", dups)
	}
}

// 值恰好等于某个键名时，不能被当成键。
func TestDuplicateKeysIgnoresStringValues(t *testing.T) {
	src := []byte(`{"a": "a", "b": "a"}`)
	if dups := DuplicateKeys(src); len(dups) != 0 {
		t.Errorf("字符串值不是键，不该报重复: %v", dups)
	}
}

func TestDuplicateKeysNoneInCleanJSON(t *testing.T) {
	src := []byte(`{"a":1,"b":2,"c":{"d":3,"e":4}}`)
	if dups := DuplicateKeys(src); len(dups) != 0 {
		t.Errorf("干净的 JSON 不该报重复键: %v", dups)
	}
}

// Windows 上存的 UTF-8 文件几乎都带 BOM。不剥掉的话，
// 标准库会指着一个看起来完全正常的 '{' 说"这里应该是一个值"，
// 肉眼根本查不出问题。这个 bug 是跑真实二进制时发现的，单测原来漏了。
func TestBOMTolerated(t *testing.T) {
	withBOM := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"a":1}`)...)
	if p := Check(withBOM); p != nil {
		t.Errorf("带 BOM 的合法 JSON 不该报错，得到: %v", p)
	}
}

func TestBOMDoesNotBreakDuplicateCheck(t *testing.T) {
	withBOM := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"a":1,"a":2}`)...)
	if dups := DuplicateKeys(withBOM); len(dups) != 1 {
		t.Errorf("带 BOM 时也要能查出重复键，找到 %d 个", len(dups))
	}
}

func TestBOMDoesNotBreakFormat(t *testing.T) {
	withBOM := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"a":1}`)...)
	got, err := Format(withBOM, "  ")
	if err != nil {
		t.Fatalf("带 BOM 的文件应该能格式化: %v", err)
	}
	// 输出里不该再有 BOM
	if bytes.HasPrefix(got, []byte{0xEF, 0xBB, 0xBF}) {
		t.Error("格式化的输出不该再带 BOM")
	}
}

// 带 BOM 但内容为空的文件，要报"空"而不是语法错误。
func TestBOMOnlyFileIsEmpty(t *testing.T) {
	var buf bytes.Buffer
	code, err := run(nil, &buf, bytes.NewReader([]byte{0xEF, 0xBB, 0xBF}))
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Error("只有 BOM 的文件应该算失败")
	}
	if !strings.Contains(buf.String(), "空") {
		t.Errorf("应该说是空文件，实际: %s", buf.String())
	}
}

func TestFormatIndent(t *testing.T) {
	got, err := Format([]byte(`{"b":2,"a":1}`), "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "\n") {
		t.Errorf("应该展开成多行，得到 %s", got)
	}
}

func TestFormatCompact(t *testing.T) {
	got, err := Format([]byte("{\n  \"a\": 1\n}"), "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "\n") {
		t.Errorf("indent 为空时应该压成一行，得到 %s", got)
	}
}

func TestFormatRejectsBadJSON(t *testing.T) {
	if _, err := Format([]byte(`{bad}`), "  "); err == nil {
		t.Error("非法 JSON 不该能格式化")
	}
}

func TestRunValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.json")
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	code, err := run([]string{path}, &buf, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Errorf("合法文件退出码应该是 0，得到 %d\n%s", code, buf.String())
	}
}

func TestRunInvalidFileExitCode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`{"a":1,}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	code, err := run([]string{path}, &buf, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Errorf("非法文件退出码应该是 1，得到 %d", code)
	}
	if !strings.Contains(buf.String(), path) {
		t.Errorf("输出里应该带文件名，方便编辑器跳转:\n%s", buf.String())
	}
}

func TestRunReadsStdin(t *testing.T) {
	var buf bytes.Buffer
	code, err := run(nil, &buf, strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Errorf("标准输入的合法 JSON 应该通过，得到 %d\n%s", code, buf.String())
	}
}

// 空文件要单独说清楚，不能报一句"意外的结尾"让人以为文件被截断了。
func TestRunEmptyInput(t *testing.T) {
	var buf bytes.Buffer
	code, err := run(nil, &buf, strings.NewReader("   \n  "))
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Error("空输入应该算失败")
	}
	if !strings.Contains(buf.String(), "空") {
		t.Errorf("应该明确说文件是空的，实际: %s", buf.String())
	}
}

func TestRunFormatToStdout(t *testing.T) {
	var buf bytes.Buffer
	code, err := run([]string{"-fmt"}, &buf, strings.NewReader(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("退出码 %d", code)
	}
	var v map[string]any
	if err := json.Unmarshal(buf.Bytes(), &v); err != nil {
		t.Errorf("-fmt 的输出应该是合法 JSON: %v\n%s", err, buf.String())
	}
}

func TestRunFormatWriteBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	if err := os.WriteFile(path, []byte(`{"b":2,"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := run([]string{"-fmt", "-w", path}, &buf, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\n") {
		t.Errorf("文件应该被格式化成多行:\n%s", b)
	}
	// 不能留下临时文件
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("目录里应该只有 1 个文件，实际 %d 个", len(entries))
	}
}

func TestRunWRequiresFmt(t *testing.T) {
	var buf bytes.Buffer
	if _, err := run([]string{"-w", "a.json"}, &buf, strings.NewReader("")); err == nil {
		t.Error("-w 不配 -fmt 应该报错")
	}
}

func TestRunWRejectsStdin(t *testing.T) {
	var buf bytes.Buffer
	if _, err := run([]string{"-fmt", "-w"}, &buf, strings.NewReader(`{}`)); err == nil {
		t.Error("-w 用于标准输入应该报错")
	}
}

func TestRunNoDupCheck(t *testing.T) {
	src := `{"a":1,"a":2}`
	var buf bytes.Buffer
	code, _ := run(nil, &buf, strings.NewReader(src))
	if code != 1 {
		t.Error("默认应该查出重复键")
	}

	buf.Reset()
	code, _ = run([]string{"-no-dup-check"}, &buf, strings.NewReader(src))
	if code != 0 {
		t.Errorf("关掉重复检查后应该通过，得到 %d\n%s", code, buf.String())
	}
}

func TestRunQuiet(t *testing.T) {
	var buf bytes.Buffer
	code, err := run([]string{"-q"}, &buf, strings.NewReader(`{"a":1,}`))
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Error("退出码应该还是 1")
	}
	if buf.Len() != 0 {
		t.Errorf("-q 不该有输出，实际: %s", buf.String())
	}
}

// 多个文件里只要有一个坏的，整体就该失败。
func TestRunMultipleFilesWorstCode(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(good, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte(`{oops}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	code, err := run([]string{good, bad}, &buf, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Errorf("有一个坏文件就该返回 1，得到 %d", code)
	}
}

func TestRunMissingFile(t *testing.T) {
	var buf bytes.Buffer
	code, err := run([]string{filepath.Join(t.TempDir(), "没有.json")}, &buf,
		strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Error("文件不存在应该返回 1")
	}
}

func TestIsWide(t *testing.T) {
	wide := []rune{'中', '文', '，', 'あ', '한'}
	narrow := []rune{'a', '1', ' ', '_', '{'}
	for _, r := range wide {
		if !isWide(r) {
			t.Errorf("%c 应该算宽字符", r)
		}
	}
	for _, r := range narrow {
		if isWide(r) {
			t.Errorf("%c 不该算宽字符", r)
		}
	}
}
