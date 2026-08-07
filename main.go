// go-lintjson：校验 JSON，并把错误位置说清楚。
//
// encoding/json 报错只给一个字节偏移（"invalid character '}' at offset 431"），
// 面对一个几百行的配置文件，还得自己数到第 431 个字节去。
// 这个工具直接告诉你第几行第几列、那一行长什么样、以及大概率是哪里写错了。
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	code, err := run(os.Args[1:], os.Stdout, os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

// run 返回进程退出码：0 全部通过，1 发现问题。
func run(args []string, out io.Writer, in io.Reader) (int, error) {
	fs := flag.NewFlagSet("go-lintjson", flag.ContinueOnError)
	fs.SetOutput(out)
	var (
		fix    = fs.Bool("fmt", false, "格式化并输出（不改原文件）")
		write  = fs.Bool("w", false, "配合 -fmt，直接写回原文件")
		indent = fs.String("indent", "  ", "缩进字符，设成空字符串则压缩成一行")
		quiet  = fs.Bool("q", false, "只用退出码表示结果，不输出内容")
		nodup  = fs.Bool("no-dup-check", false, "不检查重复键")
	)
	fs.Usage = func() {
		fmt.Fprintln(out, "用法: go-lintjson [选项] <文件...>")
		fmt.Fprintln(out, "      不传文件时从标准输入读")
		fmt.Fprintln(out)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 1, err
	}

	files := fs.Args()
	if *write && !*fix {
		return 1, fmt.Errorf("-w 需要配合 -fmt 使用")
	}
	if *write && len(files) == 0 {
		return 1, fmt.Errorf("-w 需要指定文件，不能用于标准输入")
	}

	// 没给文件就读标准输入
	if len(files) == 0 {
		src, err := io.ReadAll(in)
		if err != nil {
			return 1, err
		}
		return checkOne("(标准输入)", src, out, opts{
			fix: *fix, indent: *indent, quiet: *quiet, nodup: *nodup,
		})
	}

	worst := 0
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			fmt.Fprintln(out, "读取失败:", err)
			worst = 1
			continue
		}
		o := opts{fix: *fix, indent: *indent, quiet: *quiet, nodup: *nodup}
		if *write {
			o.writeTo = name
		}
		code, err := checkOne(name, src, out, o)
		if err != nil {
			return 1, err
		}
		if code > worst {
			worst = code
		}
	}
	return worst, nil
}

type opts struct {
	fix     bool
	indent  string
	quiet   bool
	nodup   bool
	writeTo string // 非空表示格式化后写回这个文件
}

func checkOne(name string, src []byte, out io.Writer, o opts) (int, error) {
	// 只有 BOM 没有内容的文件也算空，先剥掉再判断。
	body, _ := StripBOM(src)

	// 空文件单独说一句。json.Unmarshal 对空输入报
	// "unexpected end of JSON input"，看着像文件被截断了，其实是压根没内容。
	if len(strings.TrimSpace(string(body))) == 0 {
		if !o.quiet {
			fmt.Fprintf(out, "%s: 文件是空的\n", name)
		}
		return 1, nil
	}

	if p := Check(src); p != nil {
		if !o.quiet {
			fmt.Fprintf(out, "%s:%d:%d: %s\n", name, p.Line, p.Col, p.Msg)
			if p.Snippet != "" {
				fmt.Fprintf(out, "  %s\n  %s\n", p.Snippet, p.Caret)
			}
		}
		return 1, nil
	}

	code := 0
	if !o.nodup {
		for _, p := range DuplicateKeys(src) {
			if !o.quiet {
				fmt.Fprintf(out, "%s:%d:%d: %s\n", name, p.Line, p.Col, p.Msg)
				if p.Snippet != "" {
					fmt.Fprintf(out, "  %s\n  %s\n", p.Snippet, p.Caret)
				}
			}
			code = 1
		}
	}

	if o.fix {
		formatted, err := Format(src, o.indent)
		if err != nil {
			return 1, err
		}
		if o.writeTo != "" {
			if err := writeAtomic(o.writeTo, append(formatted, '\n')); err != nil {
				return 1, err
			}
			if !o.quiet {
				fmt.Fprintf(out, "%s: 已格式化\n", name)
			}
		} else {
			fmt.Fprintln(out, string(formatted))
		}
		return code, nil
	}

	if !o.quiet && code == 0 {
		fmt.Fprintf(out, "%s: 没问题\n", name)
	}
	return code, nil
}

// writeAtomic 原子地覆盖文件。
// 先写临时文件再改名，避免写到一半出错毁掉原文件。
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".json-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // 出了错就把临时文件清掉；改名成功后 Remove 会报错但无所谓

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if st, err := os.Stat(path); err == nil {
		if err := os.Chmod(tmp, st.Mode().Perm()); err != nil {
			return err
		}
	}
	// Windows 上 os.Rename 不覆盖已有文件，先删掉目标
	os.Remove(path)
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return nil
}
