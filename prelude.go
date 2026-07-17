package main

// prelude is the runtime support source emitted alongside every transpiled
// go++ program (as gopp_prelude.go). It provides the stdlib enums
// (Result, Option), duration shorthands, and the timer helper used by
// after() match arms. The transpiler hardcodes matching knowledge of the
// tag/field layout below in newTr().
const prelude = `package main

import "time"

// duration shorthands so "16 * ms" style code works
var ms = time.Millisecond
var second = time.Second
var minute = time.Minute

func goppAfter(d time.Duration) <-chan time.Time { return time.After(d) }

// Result[T, E] — the go++ replacement for (T, error) returns.

type __gopp_tag_Result int

const (
	__gopp_tag_Result_Ok __gopp_tag_Result = iota
	__gopp_tag_Result_Err
)

type Result[T any, E any] struct {
	__gopp_tag     __gopp_tag_Result
	__gopp_F_Ok_0  T
	__gopp_F_Err_0 E
}

func Ok[T, E any](v T) Result[T, E] {
	var z Result[T, E]
	z.__gopp_tag = __gopp_tag_Result_Ok
	z.__gopp_F_Ok_0 = v
	return z
}

func Err[T, E any](e E) Result[T, E] {
	var z Result[T, E]
	z.__gopp_tag = __gopp_tag_Result_Err
	z.__gopp_F_Err_0 = e
	return z
}

func (r Result[T, E]) IsOk() bool  { return r.__gopp_tag == __gopp_tag_Result_Ok }
func (r Result[T, E]) IsErr() bool { return r.__gopp_tag == __gopp_tag_Result_Err }

// Option[T] — the go++ replacement for nil pointers.

type __gopp_tag_Option int

const (
	__gopp_tag_Option_Some __gopp_tag_Option = iota
	__gopp_tag_Option_None
)

type Option[T any] struct {
	__gopp_tag      __gopp_tag_Option
	__gopp_F_Some_0 T
}

func Some[T any](v T) Option[T] {
	var z Option[T]
	z.__gopp_tag = __gopp_tag_Option_Some
	z.__gopp_F_Some_0 = v
	return z
}

func None[T any]() Option[T] {
	var z Option[T]
	z.__gopp_tag = __gopp_tag_Option_None
	return z
}
`
