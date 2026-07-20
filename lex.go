package main

import (
	"fmt"
	"strings"
)

type tokKind int

const (
	kEOF tokKind = iota
	kIdent
	kInt
	kFloat
	kString
	kRune
	kOp
	kNewline
)

type token struct {
	kind tokKind
	text string
	line int
	col  int // 1-based byte column of the token's first byte
}

var ops3 = []string{"<<=", ">>=", "&^=", "..."}
var ops2 = []string{"<<", ">>", "&^", "==", "!=", "<=", ">=", "&&", "||", "<-", "->", "++", "--", ":=", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^="}

const ops1 = "+-*/%&|^<>=!~()[]{},;.:@#?"

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func lex(src string) ([]token, error) {
	var toks []token
	line := 1
	lineStart := 0 // byte offset of the current line's first byte
	i, n := 0, len(src)
	// emit is always called with i at the token's first byte
	emit := func(k tokKind, s string) { toks = append(toks, token{k, s, line, i - lineStart + 1}) }
	for i < n {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '\n':
			emit(kNewline, "\n")
			line++
			i++
			lineStart = i
		case c == '/' && i+1 < n && src[i+1] == '/':
			for i < n && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && src[i+1] == '*':
			i += 2
			nl := false
			for i+1 < n && !(src[i] == '*' && src[i+1] == '/') {
				if src[i] == '\n' {
					nl = true
					line++
					lineStart = i + 1
				}
				i++
			}
			if i+1 >= n {
				return nil, fmt.Errorf("line %d: unterminated block comment", line)
			}
			i += 2
			if nl {
				emit(kNewline, "\n")
			}
		case isAlpha(c) || c == '_':
			j := i + 1
			for j < n && (isAlpha(src[j]) || isDigit(src[j]) || src[j] == '_') {
				j++
			}
			emit(kIdent, src[i:j])
			i = j
		case isDigit(c):
			j := i
			isF := false
			for j < n {
				d := src[j]
				if isDigit(d) || isAlpha(d) || d == '_' {
					j++
					continue
				}
				if d == '.' && j+1 < n && src[j+1] != '.' {
					isF = true
					j++
					continue
				}
				if (d == '+' || d == '-') && j > i &&
					(src[j-1] == 'e' || src[j-1] == 'E' || src[j-1] == 'p' || src[j-1] == 'P') {
					isF = true
					j++
					continue
				}
				break
			}
			if isF {
				emit(kFloat, src[i:j])
			} else {
				emit(kInt, src[i:j])
			}
			i = j
		case c == '.' && i+1 < n && isDigit(src[i+1]):
			j := i + 1
			for j < n && isDigit(src[j]) {
				j++
			}
			emit(kFloat, src[i:j])
			i = j
		case c == '"':
			j := i + 1
			for j < n && src[j] != '"' {
				if src[j] == '\\' {
					j++
				}
				if j < n && src[j] == '\n' {
					return nil, fmt.Errorf("line %d: newline in string literal", line)
				}
				j++
			}
			if j >= n {
				return nil, fmt.Errorf("line %d: unterminated string literal", line)
			}
			emit(kString, src[i:j+1])
			i = j + 1
		case c == '`':
			j := i + 1
			for j < n && src[j] != '`' {
				if src[j] == '\n' {
					line++
					lineStart = j + 1
				}
				j++
			}
			if j >= n {
				return nil, fmt.Errorf("line %d: unterminated raw string literal", line)
			}
			emit(kString, src[i:j+1])
			i = j + 1
		case c == '\'':
			j := i + 1
			for j < n && src[j] != '\'' {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			if j >= n {
				return nil, fmt.Errorf("line %d: unterminated rune literal", line)
			}
			emit(kRune, src[i:j+1])
			i = j + 1
		default:
			matched := ""
			for _, op := range ops3 {
				if i+3 <= n && src[i:i+3] == op {
					matched = op
					break
				}
			}
			if matched == "" {
				for _, op := range ops2 {
					if i+2 <= n && src[i:i+2] == op {
						matched = op
						break
					}
				}
			}
			if matched == "" && strings.IndexByte(ops1, c) >= 0 {
				matched = string(c)
			}
			if matched == "" {
				return nil, fmt.Errorf("line %d: unexpected character %q", line, c)
			}
			emit(kOp, matched)
			i += len(matched)
		}
	}
	toks = append(toks, token{kEOF, "", line, i - lineStart + 1})
	return toks, nil
}
