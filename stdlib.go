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
	"str":      {strGopp, strGo},
	"conv":     {convGopp, convGo},
	"math":     {mathGopp, mathGo},
	"os":       {osGopp, osGo},
	"time":     {timeGopp, timeGo},
	"sort":     {sortGopp, sortGo},
	"rand":     {randGopp, randGo},
	"filepath": {filepathGopp, filepathGo},
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

const mathGopp = `package math

// Floating-point math (float64), backed by Go's math package.

func Sqrt(x float64) float64 = native
func Pow(x float64, y float64) float64 = native
func Floor(x float64) float64 = native
func Ceil(x float64) float64 = native
func Abs(x float64) float64 = native
func Min(x float64, y float64) float64 = native
func Max(x float64, y float64) float64 = native
`

const mathGo = `package math

import "math"

func Sqrt(x float64) float64       { return math.Sqrt(x) }
func Pow(x float64, y float64) float64 { return math.Pow(x, y) }
func Floor(x float64) float64      { return math.Floor(x) }
func Ceil(x float64) float64       { return math.Ceil(x) }
func Abs(x float64) float64        { return math.Abs(x) }
func Min(x float64, y float64) float64 { return math.Min(x, y) }
func Max(x float64, y float64) float64 { return math.Max(x, y) }
`

const osGopp = `package os

// Operating-system interaction. Failures are Result values, never
// silently dropped.

func Args() []string = native
func Getenv(key string) string = native
func Exit(code int) = native
func ReadFile(path string) Result[string, string] = native
func WriteFile(path string, content string) Result[bool, string] = native
`

const osGo = `package os

import "os"

import "goppout/gopp"

func Args() []string        { return os.Args }
func Getenv(key string) string { return os.Getenv(key) }
func Exit(code int)         { os.Exit(code) }

func ReadFile(path string) gopp.Result[string, string] {
	data, err := os.ReadFile(path)
	if err != nil {
		return gopp.Err[string, string](err.Error())
	}
	return gopp.Ok[string, string](string(data))
}

func WriteFile(path string, content string) gopp.Result[bool, string] {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return gopp.Err[bool, string](err.Error())
	}
	return gopp.Ok[bool, string](true)
}
`

const timeGopp = `package time

// Clock, sleeping, and duration arithmetic. Durations are go++'s own
// duration type (ms / second / minute shorthands work).

func Sleep(d duration) = native
func Unix() int = native
func UnixMillis() int = native
func Since(t int) duration = native
func Add(d duration, delta duration) duration = native
func Hours(n float64) duration = native
func Minutes(n float64) duration = native
func Seconds(n float64) duration = native
`

const timeGo = `package time

import "time"

func Sleep(d time.Duration) { time.Sleep(d) }
func Unix() int             { return int(time.Now().Unix()) }
func UnixMillis() int       { return int(time.Now().UnixMilli()) }

func Since(t int) time.Duration { return time.Since(time.Unix(int64(t), 0)) }
func Add(d time.Duration, delta time.Duration) time.Duration {
	return d + delta
}
func Hours(n float64) time.Duration   { return time.Duration(n * float64(time.Hour)) }
func Minutes(n float64) time.Duration { return time.Duration(n * float64(time.Minute)) }
func Seconds(n float64) time.Duration { return time.Duration(n * float64(time.Second)) }
`

const sortGopp = `package sort

// Sorting slices in place.

func Ints(xs []int) = native
func Floats(xs []float64) = native
func Strings(xs []string) = native
func IntsDesc(xs []int) = native
`

const sortGo = `package sort

import "sort"

func Ints(xs []int)        { sort.Ints(xs) }
func Floats(xs []float64)  { sort.Float64s(xs) }
func Strings(xs []string)  { sort.Strings(xs) }
func IntsDesc(xs []int)    { sort.Sort(sort.Reverse(sort.IntSlice(xs))) }
`

const randGopp = `package rand

// Pseudo-random numbers (Go's math/rand/v2).

func Intn(n int) int = native
func Float64() float64 = native
func Shuffle(xs []int) = native
`

const randGo = `package rand

import "math/rand/v2"

func Intn(n int) int     { return rand.IntN(n) }
func Float64() float64   { return rand.Float64() }
func Shuffle(xs []int)   { rand.Shuffle(len(xs), func(i, j int) { xs[i], xs[j] = xs[j], xs[i] }) }
`

const filepathGopp = `package filepath

// Filesystem path manipulation, backed by Go's path/filepath.

func Join(parts []string) string = native
func Base(path string) string = native
func Dir(path string) string = native
func Ext(path string) string = native
func Clean(path string) string = native
func IsAbs(path string) bool = native
`

const filepathGo = `package filepath

import "path/filepath"

func Join(parts []string) string { return filepath.Join(parts...) }
func Base(path string) string    { return filepath.Base(path) }
func Dir(path string) string     { return filepath.Dir(path) }
func Ext(path string) string     { return filepath.Ext(path) }
func Clean(path string) string   { return filepath.Clean(path) }
func IsAbs(path string) bool     { return filepath.IsAbs(path) }
`
