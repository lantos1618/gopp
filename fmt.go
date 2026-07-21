package main

// fmt.go — go++ source formatter: canonical indentation.
//
// Deliberately a REINDENTER, not a pretty-printer: it never rewraps or
// respaces tokens, so comments and code survive byte-for-byte; only
// leading whitespace changes. Depth comes from { } outside strings and
// comments; a line starting with closers dedents first. Four spaces per
// level, matching the examples (and gofmt's visual width).

import (
	"fmt"
	"os"
	"strings"
)

const indentUnit = "    "

// formatSource reindents src and returns the result.
func formatSource(src string) string {
	lines := strings.Split(src, "\n")
	var b strings.Builder
	depth := 0
	inBlock := false // inside a /* */ comment
	for _, raw := range lines {
		trim := strings.TrimSpace(raw)
		if trim == "" {
			b.WriteByte('\n')
			continue
		}
		opens, closes, leadClose := braceDelta(trim, &inBlock)
		eff := depth - leadClose
		if eff < 0 {
			eff = 0 // unbalanced input: degrade gracefully, don't go negative
		}
		b.WriteString(strings.Repeat(indentUnit, eff))
		b.WriteString(trim)
		b.WriteByte('\n')
		depth += opens - closes
		if depth < 0 {
			depth = 0
		}
	}
	return b.String()
}

// braceDelta scans one line counting { } outside strings and comments,
// and reports how many } lead the line. inBlock carries /* */ state
// across lines.
func braceDelta(line string, inBlock *bool) (opens, closes, leadClose int) {
	lead := true
	for i := 0; i < len(line); i++ {
		c := line[i]
		if *inBlock {
			if c == '*' && i+1 < len(line) && line[i+1] == '/' {
				*inBlock = false
				i++
			}
			continue
		}
		switch c {
		case '/':
			if i+1 < len(line) && line[i+1] == '/' {
				return opens, closes, leadClose // line comment: rest is text
			}
			if i+1 < len(line) && line[i+1] == '*' {
				*inBlock = true
				i++
			}
		case '"', '\'':
			// skip the string/rune literal (go++ literals are single-line)
			for i++; i < len(line) && line[i] != c; i++ {
				if line[i] == '\\' {
					i++
				}
			}
		case '{':
			opens++
			lead = false
		case '}':
			closes++
			if lead {
				leadClose++
			}
		default:
			if c != ' ' && c != '\t' {
				lead = false
			}
		}
	}
	return opens, closes, leadClose
}

// runFmt implements `gopp fmt [-w] files...`.
func runFmt(args []string) {
	write := false
	var files []string
	for _, a := range args {
		if a == "-w" {
			write = true
		} else {
			files = append(files, a)
		}
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "usage: gopp fmt [-w] <file.gopp>...")
		os.Exit(2)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			fatal(err)
		}
		out := formatSource(string(data))
		if write {
			if string(data) != out {
				if err := os.WriteFile(f, []byte(out), 0o644); err != nil {
					fatal(err)
				}
				fmt.Println("formatted", f)
			}
		} else {
			fmt.Print(out)
		}
	}
}
