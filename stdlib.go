package main

// stdlib.go — the embedded standard library.
//
// A stdlib package is a pair of sources: a .gopp file declaring the API
// (`= native` functions have no body) and a .go file implementing it.
// The loader (pkg.go) falls back to this registry when an import path
// names no directory on disk; emitGraph writes the native .go alongside
// the transpiled declarations. `new` in these sources would be a Go
// keyword — parameter names avoid it.

type stdlibPkg struct {
	src  string // go++ declarations
	impl string // Go implementation (native.go)
}

var stdlibPackages = map[string]stdlibPkg{
	"str":  {strGopp, strGo},
	"conv": {convGopp, convGo},
}

const strGopp = `package str

// String utilities, backed by Go's strings package.

func ToUpper(s string) string = native
func ToLower(s string) string = native
func Trim(s string) string = native
func Contains(s string, sub string) bool = native
func HasPrefix(s string, p string) bool = native
func HasSuffix(s string, p string) bool = native
func Replace(s string, old string, replacement string) string = native
func Repeat(s string, n int) string = native
func Split(s string, sep string) []string = native
func Join(parts []string, sep string) string = native
`

const strGo = `package str

import "strings"

func ToUpper(s string) string { return strings.ToUpper(s) }
func ToLower(s string) string { return strings.ToLower(s) }
func Trim(s string) string    { return strings.TrimSpace(s) }

func Contains(s string, sub string) bool   { return strings.Contains(s, sub) }
func HasPrefix(s string, p string) bool    { return strings.HasPrefix(s, p) }
func HasSuffix(s string, p string) bool    { return strings.HasSuffix(s, p) }
func Repeat(s string, n int) string        { return strings.Repeat(s, n) }
func Split(s string, sep string) []string  { return strings.Split(s, sep) }
func Join(parts []string, sep string) string {
	return strings.Join(parts, sep)
}

func Replace(s string, old string, replacement string) string {
	return strings.ReplaceAll(s, old, replacement)
}
`

const convGopp = `package conv

// Conversions between strings and numbers.

func Itoa(i int) string = native
func Atoi(s string) Result[int, string] = native
`

const convGo = `package conv

import (
	"strconv"

	"goppout/gopp"
)

func Itoa(i int) string { return strconv.Itoa(i) }

func Atoi(s string) gopp.Result[int, string] {
	i, err := strconv.Atoi(s)
	if err != nil {
		return gopp.Err[int, string](err.Error())
	}
	return gopp.Ok[int, string](i)
}
`
