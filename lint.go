package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Problem 是一处问题。Line/Col 从 1 开始，符合编辑器的习惯。
type Problem struct {
	Line int
	Col  int
	Msg  string
	// Snippet 是出错那一行的内容，Caret 是指向出错列的那行 ^
	Snippet string
	Caret   string
}

func (p Problem) String() string {
	return fmt.Sprintf("第 %d 行 第 %d 列: %s", p.Line, p.Col, p.Msg)
}

// bom 是 UTF-8 的字节顺序标记。
var bom = []byte{0xEF, 0xBB, 0xBF}

// StripBOM 去掉开头的 BOM，并返回去掉了几个字节。
//
// Windows 上用记事本、PowerShell 的 Set-Content、以及不少编辑器
// 存 UTF-8 文件都会带 BOM。encoding/json 不认它，会在第 1 行第 1 列
// 报一句"这里应该是一个值"——指着一个看起来完全正常的 '{'。
// 这种报错最坑人，因为肉眼根本看不出问题在哪。
func StripBOM(src []byte) ([]byte, int) {
	if bytes.HasPrefix(src, bom) {
		return src[len(bom):], len(bom)
	}
	return src, 0
}

// Check 校验一段 JSON，返回语法错误（最多一个，因为解析器遇错就停）。
func Check(src []byte) *Problem {
	src, _ = StripBOM(src)

	var v any
	err := json.Unmarshal(src, &v)
	if err == nil {
		return nil
	}

	offset := errOffset(err)
	line, col := lineCol(src, offset)
	p := &Problem{
		Line: line,
		Col:  col,
		Msg:  humanize(err, src, offset),
	}
	p.Snippet, p.Caret = snippet(src, line, col)
	return p
}

// errOffset 从 json 的错误里抠出字节偏移。
//
// encoding/json 把位置信息藏在两个不同的类型里，而且 UnmarshalTypeError
// 的 Offset 指向的是**值结束之后**，SyntaxError 指向的是出错处。
// 不区分的话报出来的位置会差一截。
func errOffset(err error) int64 {
	switch e := err.(type) {
	case *json.SyntaxError:
		// Offset 指向出错字符的后一个位置，减 1 才是真正出问题的地方。
		if e.Offset > 0 {
			return e.Offset - 1
		}
		return 0
	case *json.UnmarshalTypeError:
		return e.Offset
	}
	return 0
}

// lineCol 把字节偏移换算成行列。
//
// 列号按**字符**数而不是字节数算。中文一个字 3 字节，
// 按字节报列的话，含中文的行报出来的列号会大得离谱，对不上编辑器。
func lineCol(src []byte, offset int64) (line, col int) {
	if offset < 0 {
		offset = 0
	}
	if offset > int64(len(src)) {
		offset = int64(len(src))
	}

	line, col = 1, 1
	for i := int64(0); i < offset; {
		if src[i] == '\n' {
			line++
			col = 1
			i++
			continue
		}
		_, size := utf8.DecodeRune(src[i:])
		if size == 0 {
			size = 1
		}
		i += int64(size)
		col++
	}
	return line, col
}

// snippet 取出某一行，并生成指向指定列的 ^ 标记行。
func snippet(src []byte, line, col int) (text, caret string) {
	lines := strings.Split(string(src), "\n")
	if line < 1 || line > len(lines) {
		return "", ""
	}
	text = strings.TrimRight(lines[line-1], "\r")

	// caret 的缩进要按**显示宽度**算，不能简单重复 col-1 个空格。
	// 一是 tab 要原样保留（否则 ^ 会错位），二是中文占两格宽。
	var b strings.Builder
	n := 0
	for _, r := range text {
		if n >= col-1 {
			break
		}
		if r == '\t' {
			b.WriteRune('\t')
		} else if isWide(r) {
			b.WriteString("  ")
		} else {
			b.WriteByte(' ')
		}
		n++
	}
	return text, b.String() + "^"
}

// isWide 粗略判断一个字符在终端里是不是占两列。
// 只覆盖常见的 CJK 区间，够用了。
func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // 韩文字母
		r >= 0x2E80 && r <= 0xA4CF, // CJK 部首、汉字
		r >= 0xAC00 && r <= 0xD7A3, // 韩文音节
		r >= 0xF900 && r <= 0xFAFF, // CJK 兼容
		r >= 0xFE30 && r <= 0xFE6F, // CJK 标点
		r >= 0xFF00 && r <= 0xFF60, // 全角
		r >= 0xFFE0 && r <= 0xFFE6:
		return true
	}
	return false
}

// humanize 把 encoding/json 那句干巴巴的英文错误翻译成能看懂的话，
// 并尽量猜出用户到底写错了什么。
//
// 原始信息类似 "invalid character '}' looking for beginning of object key string"，
// 对着这句话很难反应过来"哦是多打了个逗号"。
func humanize(err error, src []byte, offset int64) string {
	se, ok := err.(*json.SyntaxError)
	if !ok {
		if te, ok := err.(*json.UnmarshalTypeError); ok {
			return fmt.Sprintf("类型不对：%s 不能当作 %s", te.Value, te.Type)
		}
		return err.Error()
	}

	raw := se.Error()
	ch := charAt(src, offset)

	switch {
	case strings.Contains(raw, "looking for beginning of object key string"):
		switch ch {
		case '}':
			return "对象里多了一个逗号（最后一项后面不能有逗号）"
		case '\'':
			return "键名要用双引号，JSON 不支持单引号"
		case '/', '#':
			// 注释既可能出现在值的位置，也可能出现在键的位置，两边都要认。
			return "JSON 不支持注释"
		}
		return "这里应该是一个用双引号括起来的键名"

	case strings.Contains(raw, "looking for beginning of value"):
		switch ch {
		case ']':
			return "数组里多了一个逗号（最后一项后面不能有逗号）"
		case '}':
			return "这里少了一个值"
		case '\'':
			return "字符串要用双引号，JSON 不支持单引号"
		case '/':
			return "JSON 不支持注释"
		}
		if word := wordAt(src, offset); word != "" {
			switch word {
			case "None", "True", "False":
				return fmt.Sprintf("%s 是 Python 写法，JSON 要用 null / true / false", word)
			case "undefined", "NaN", "Infinity":
				return fmt.Sprintf("JSON 不支持 %s", word)
			}
			return fmt.Sprintf("%q 不是合法的值，字符串需要用双引号括起来", word)
		}
		return "这里应该是一个值"

	case strings.Contains(raw, "after object key:value pair"):
		return "两个键值对之间少了逗号"

	case strings.Contains(raw, "after array element"):
		return "两个数组元素之间少了逗号"

	case strings.Contains(raw, "after top-level value"):
		return "顶层已经是一个完整的值了，后面多出了内容（JSON 只能有一个根）"

	case strings.Contains(raw, "after object key"):
		return "键名后面应该是冒号"

	case strings.Contains(raw, "unexpected end of JSON input"):
		return "内容不完整，可能有括号或引号没闭合"

	case strings.Contains(raw, "invalid character"):
		if ch == '\'' {
			return "JSON 不支持单引号，请改成双引号"
		}
		return fmt.Sprintf("这里出现了不该有的字符 %q", string(ch))
	}
	return raw
}

func charAt(src []byte, offset int64) rune {
	if offset < 0 || offset >= int64(len(src)) {
		return 0
	}
	r, _ := utf8.DecodeRune(src[offset:])
	return r
}

// wordAt 取出 offset 处的一个"词"，用来识别 None/True/undefined 这类裸标识符。
func wordAt(src []byte, offset int64) string {
	if offset < 0 || offset >= int64(len(src)) {
		return ""
	}
	i := int(offset)
	j := i
	for j < len(src) {
		c := src[j]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' {
			j++
			continue
		}
		break
	}
	if j == i {
		return ""
	}
	return string(src[i:j])
}

// DuplicateKeys 找出重复的键。
//
// encoding/json 遇到重复键是**静默后覆盖**的，不报任何错。
// 这在配置文件里是个很阴的坑：你改了前面那个 "port"，
// 结果生效的是后面那个，怎么调都不对。标准解析器不管，所以单独扫一遍。
func DuplicateKeys(src []byte) []Problem {
	src, _ = StripBOM(src)
	dec := json.NewDecoder(strings.NewReader(string(src)))
	var out []Problem

	// 用一个栈跟踪当前所在的对象层级，每层记已经见过的键。
	type frame struct {
		isObject bool
		seen     map[string]int // 键 -> 首次出现的行号
	}
	var stack []frame

	for {
		tok, err := dec.Token()
		if err != nil {
			// 读完或者语法错误都在这退出。语法错误由 Check 负责报。
			break
		}

		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				stack = append(stack, frame{isObject: true, seen: map[string]int{}})
			case '[':
				stack = append(stack, frame{})
			case '}', ']':
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
			}
		case string:
			// 只有在对象里、且当前该读键的位置，这个字符串才是键。
			// Decoder 没直接告诉我们这一点，靠 More() 之外的办法不好判断，
			// 所以这里用一个简化规则：对象层里出现的字符串，
			// 奇数次是键、偶数次是值。用 seen 的计数来区分。
			if len(stack) == 0 || !stack[len(stack)-1].isObject {
				continue
			}
			f := &stack[len(stack)-1]
			if f.seen == nil {
				f.seen = map[string]int{}
			}
			// 通过 offset 定位；Decoder 的 InputOffset 指向 token 结束处。
			off := dec.InputOffset()
			if !isKeyPosition(src, off) {
				continue
			}
			line, col := lineCol(src, off-int64(len(t))-2)
			if first, dup := f.seen[t]; dup {
				p := Problem{
					Line: line, Col: col,
					Msg: fmt.Sprintf("键 %q 重复了（第 %d 行已经出现过），后面的会覆盖前面的", t, first),
				}
				p.Snippet, p.Caret = snippet(src, line, col)
				out = append(out, p)
			} else {
				f.seen[t] = line
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Col < out[j].Col
	})
	return out
}

// isKeyPosition 判断刚读完的字符串后面是不是跟着冒号——是的话它就是个键。
func isKeyPosition(src []byte, afterToken int64) bool {
	for i := afterToken; i < int64(len(src)); i++ {
		switch src[i] {
		case ' ', '\t', '\r', '\n':
			continue
		case ':':
			return true
		default:
			return false
		}
	}
	return false
}

// Format 重新排版 JSON。indent 传空字符串表示压缩成一行。
func Format(src []byte, indent string) ([]byte, error) {
	src, _ = StripBOM(src)
	var v any
	if err := json.Unmarshal(src, &v); err != nil {
		return nil, err
	}
	if indent == "" {
		return json.Marshal(v)
	}
	return json.MarshalIndent(v, "", indent)
}
