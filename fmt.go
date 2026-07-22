package main

// fmt.go — go++ source formatter: canonical indentation + whitespace
// hygiene.
//
// Deliberately a REINDENTER, not a pretty-printer: it never rewraps or
// respaces tokens, so comments and code survive byte-for-byte; only
// leading whitespace changes. Depth comes from { } outside strings and
// comments; a line starting with closers dedents first. Four spaces per
// level, matching the examples (and gofmt's visual width).
//
// Whitespace hygiene (all token-safe):
//   - trailing whitespace is stripped on every line
//   - runs of 2+ blank lines collapse to one
//   - the file ends with exactly one newline
//   - a lone "}" followed on the next line by "else" joins into
//     "} else ..." — only when nothing but whitespace separates them
//     (never across a comment)

import (
	"fmt"
	"os"
	"strings"
)

const indentUnit = "    "

// formatSource reindents src, applies the whitespace-hygiene rules
// listed above, and returns the result. It is idempotent:
// formatSource(formatSource(x)) == formatSource(x).
func formatSource(src string) string {
	lines := strings.Split(src, "\n")
	// Strip trailing whitespace on every line (\r covers CRLF input).
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t\r")
	}
	lines = joinElse(lines)
	// Drop trailing blank lines; the emit loop below terminates the last
	// content line with '\n', leaving exactly one final newline.
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var b strings.Builder
	depth := 0
	inBlock := false // inside a /* */ comment
	blank := false   // previous emitted line was blank: collapse runs
	for _, raw := range lines {
		trim := strings.TrimSpace(raw)
		if trim == "" {
			if !blank {
				b.WriteByte('\n')
			}
			blank = true
			continue
		}
		blank = false
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

// joinElse folds a line that is exactly "}" together with a following
// line that starts with "else" into one "} else ..." line. The previous
// line must be a bare "}" (a trailing comment like "} // x" blocks the
// join) and the "else" must be on the immediately next line, so the two
// are never joined across a comment or a blank line. The joined line
// keeps the "}" line's leading whitespace.
func joinElse(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "}" && isElseLine(line) {
			prev := out[len(out)-1]
			indent := prev[:len(prev)-len(strings.TrimLeft(prev, " \t"))]
			out[len(out)-1] = indent + "} " + strings.TrimSpace(line)
			continue
		}
		out = append(out, line)
	}
	return out
}

// isElseLine reports whether line, ignoring surrounding whitespace,
// starts with the keyword else (else { ... } / else if ...).
func isElseLine(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "else") {
		return false
	}
	if len(t) == len("else") {
		return true
	}
	switch t[len("else")] {
	case ' ', '\t', '{':
		return true
	}
	return false
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
