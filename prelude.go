package main

// prelude is the runtime support source emitted as the shared package
// "gopp" (outdir/gopp/gopp.go) for every transpiled go++ program. It
// provides the stdlib enums (Result, Option), duration shorthands, and
// the timer helper used by after() match arms. Everything cross-package
// is exported; the tag/field layout below is hardcoded in emit.go
// (tagConst, fieldGo, the try desugar).
const prelude = `package gopp

import (
	"fmt"
	"strings"
	"time"
)

// duration shorthands so "16 * ms" style code works
var Ms = time.Millisecond
var Second = time.Second
var Minute = time.Minute

func GoppAfter(d time.Duration) <-chan time.Time { return time.After(d) }

// go++'s println/print: real stdout, %v formatting (Go's builtin println
// writes to stderr and prints floats as exponents).
func Println(a ...any) { fmt.Println(a...) }
func Print(a ...any)   { fmt.Print(a...) }

// Str concatenates string-interpolation parts ("hi {name}!") with no
// added spacing.
func Str(parts ...any) string {
	var b strings.Builder
	for _, p := range parts {
		fmt.Fprint(&b, p)
	}
	return b.String()
}

// language string builtins — the same names work at comptime (meta.go)
// and at runtime (these), like Zig's std functions.
func HasPrefix(s string, p string) bool { return strings.HasPrefix(s, p) }
func HasSuffix(s string, p string) bool { return strings.HasSuffix(s, p) }
func Contains(s string, sub string) bool { return strings.Contains(s, sub) }
func Replace(s string, old string, replacement string) string {
	return strings.ReplaceAll(s, old, replacement)
}
func Split(s string, sep string) []string      { return strings.Split(s, sep) }
func Join(parts []string, sep string) string   { return strings.Join(parts, sep) }
func Upper(s string) string                    { return strings.ToUpper(s) }
func Lower(s string) string                    { return strings.ToLower(s) }
func Trim(s string) string                     { return strings.TrimSpace(s) }
func Repeat(s string, n int) string            { return strings.Repeat(s, n) }

// test assertions (gopp test): panic on failure, the runner recovers.
func Assert(cond bool) {
	if !cond {
		panic("assertion failed")
	}
}

func AssertEq[T comparable](a, b T) {
	if a != b {
		panic(fmt.Sprintf("assertEq failed: %v != %v", a, b))
	}
}

// Result[T, E] — the go++ replacement for (T, error) returns.

type gopp_tag_Result int

const (
	Gopp_Tag_Result_Ok gopp_tag_Result = iota
	Gopp_Tag_Result_Err
)

type Result[T any, E any] struct {
	Gopp_Tag     gopp_tag_Result
	Gopp_F_Ok_v0  T
	Gopp_F_Err_v0 E
}

func Ok[T, E any](v T) Result[T, E] {
	var z Result[T, E]
	z.Gopp_Tag = Gopp_Tag_Result_Ok
	z.Gopp_F_Ok_v0 = v
	return z
}

func Err[T, E any](e E) Result[T, E] {
	var z Result[T, E]
	z.Gopp_Tag = Gopp_Tag_Result_Err
	z.Gopp_F_Err_v0 = e
	return z
}

func (r Result[T, E]) IsOk() bool  { return r.Gopp_Tag == Gopp_Tag_Result_Ok }
func (r Result[T, E]) IsErr() bool { return r.Gopp_Tag == Gopp_Tag_Result_Err }

// Option[T] — the go++ replacement for nil pointers.

type gopp_tag_Option int

const (
	Gopp_Tag_Option_Some gopp_tag_Option = iota
	Gopp_Tag_Option_None
)

type Option[T any] struct {
	Gopp_Tag      gopp_tag_Option
	Gopp_F_Some_v0 T
}

func Some[T any](v T) Option[T] {
	var z Option[T]
	z.Gopp_Tag = Gopp_Tag_Option_Some
	z.Gopp_F_Some_v0 = v
	return z
}

func None[T any]() Option[T] {
	var z Option[T]
	z.Gopp_Tag = Gopp_Tag_Option_None
	return z
}
`
