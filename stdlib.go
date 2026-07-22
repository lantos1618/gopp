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
	"json":     {jsonGopp, jsonGo},
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
func Split(s string, sep string) [string] = native
func Join(parts [string], sep string) string = native
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

func Args() [string] = native
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

func Ints(xs [int]) = native
func Floats(xs [float64]) = native
func Strings(xs [string]) = native
func IntsDesc(xs [int]) = native
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
func Shuffle(xs [int]) = native
`

const randGo = `package rand

import "math/rand/v2"

func Intn(n int) int     { return rand.IntN(n) }
func Float64() float64   { return rand.Float64() }
func Shuffle(xs []int)   { rand.Shuffle(len(xs), func(i, j int) { xs[i], xs[j] = xs[j], xs[i] }) }
`

const filepathGopp = `package filepath

// Filesystem path manipulation, backed by Go's path/filepath.

func Join(parts [string]) string = native
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

const jsonGopp = `package json

// JSON: scalar emission (native) plus a full recursive-descent parser
// written in pure go++ (self-hosted decoding). Decoders per struct are
// GENERATED via comptime metaprogramming (see examples/jsondemo).

func Quote(s string) string = native
func BoolStr(b bool) string = native
func Float(f float64) string = native
func ParseF(s string) Result[float64, string] = native

enum Value {
    Null
    Bool(b bool)
    Num(f float64)
    Str(s string)
    Arr(v [Value])
    Obj(m map<string, Value>)
}

type parser struct {
    src string
    pos int
}

func Parse(s string) Result[Value, string] {
    p := parser{src: s, pos: 0}
    v := parseValue(&p)?
    skipWs(&p)
    if p.pos < len(s) {
        return Err[Value, string]("trailing data at offset {p.pos}")
    }
    return Ok[Value, string](v)
}

func skipWs(p *parser) {
    for p.pos < len(p.src) {
        c := p.src[p.pos]
        if c != byte(' ') && c != byte('\t') && c != byte('\n') && c != byte('\r') {
            return
        }
        p.pos++
    }
}

func parseValue(p *parser) Result[Value, string] {
    skipWs(p)
    if p.pos >= len(p.src) {
        return Err[Value, string]("unexpected end of input")
    }
    c := p.src[p.pos]
    if c == byte('{') { return parseObject(p) }
    if c == byte('[') { return parseArray(p) }
    if c == byte('"') {
        s := parseString(p)?
        return Ok[Value, string](Str(s))
    }
    if c == byte('t') { return parseLit(p, "true", Bool(true)) }
    if c == byte('f') { return parseLit(p, "false", Bool(false)) }
    if c == byte('n') { return parseLit(p, "null", Null) }
    return parseNumber(p)
}

func parseLit(p *parser, word string, v Value) Result[Value, string] {
    if len(p.src)-p.pos < len(word) || p.src[p.pos:p.pos+len(word)] != word {
        return Err[Value, string]("invalid literal")
    }
    p.pos = p.pos + len(word)
    return Ok[Value, string](v)
}

func parseString(p *parser) Result[string, string] {
    p.pos++
    out := ""
    for p.pos < len(p.src) {
        c := p.src[p.pos]
        if c == byte('"') {
            p.pos++
            return Ok[string, string](out)
        }
        if c == byte('\\') && p.pos+1 < len(p.src) {
            n := p.src[p.pos+1]
            if n == byte('n') {
                out = out + "\n"
            } else if n == byte('t') {
                out = out + "\t"
            } else if n == byte('r') {
                out = out + "\r"
            } else {
                out = out + string(rune(n))
            }
            p.pos = p.pos + 2
            continue
        }
        out = out + string(rune(c))
        p.pos++
    }
    return Err[string, string]("unterminated string")
}

func isDigit(c byte) bool {
    return c >= byte('0') && c <= byte('9')
}

func parseNumber(p *parser) Result[Value, string] {
    start := p.pos
    if p.pos < len(p.src) && p.src[p.pos] == byte('-') { p.pos++ }
    for p.pos < len(p.src) && isDigit(p.src[p.pos]) { p.pos++ }
    if p.pos < len(p.src) && p.src[p.pos] == byte('.') {
        p.pos++
        for p.pos < len(p.src) && isDigit(p.src[p.pos]) { p.pos++ }
    }
    if p.pos < len(p.src) && (p.src[p.pos] == byte('e') || p.src[p.pos] == byte('E')) {
        p.pos++
        if p.pos < len(p.src) && (p.src[p.pos] == byte('+') || p.src[p.pos] == byte('-')) { p.pos++ }
        for p.pos < len(p.src) && isDigit(p.src[p.pos]) { p.pos++ }
    }
    if p.pos == start {
        return Err[Value, string]("invalid value at offset {start}")
    }
    f := ParseF(p.src[start:p.pos])?
    return Ok[Value, string](Num(f))
}

func parseObject(p *parser) Result[Value, string] {
    p.pos++
    var m map<string, Value>
    skipWs(p)
    if p.pos < len(p.src) && p.src[p.pos] == byte('}') {
        p.pos++
        return Ok[Value, string](Obj(m))
    }
    for {
        skipWs(p)
        if p.pos >= len(p.src) || p.src[p.pos] != byte('"') {
            return Err[Value, string]("expected a string key")
        }
        k := parseString(p)?
        skipWs(p)
        if p.pos >= len(p.src) || p.src[p.pos] != byte(':') {
            return Err[Value, string]("expected ':'")
        }
        p.pos++
        v := parseValue(p)?
        m[k] = v
        skipWs(p)
        if p.pos < len(p.src) && p.src[p.pos] == byte(',') {
            p.pos++
            continue
        }
        if p.pos < len(p.src) && p.src[p.pos] == byte('}') {
            p.pos++
            return Ok[Value, string](Obj(m))
        }
        return Err[Value, string]("expected ',' or '}}'")
    }
}

func parseArray(p *parser) Result[Value, string] {
    p.pos++
    var out [Value]
    skipWs(p)
    if p.pos < len(p.src) && p.src[p.pos] == byte(']') {
        p.pos++
        return Ok[Value, string](Arr(out))
    }
    for {
        v := parseValue(p)?
        out = append(out, v)
        skipWs(p)
        if p.pos < len(p.src) && p.src[p.pos] == byte(',') {
            p.pos++
            continue
        }
        if p.pos < len(p.src) && p.src[p.pos] == byte(']') {
            p.pos++
            return Ok[Value, string](Arr(out))
        }
        return Err[Value, string]("expected ',' or ']'")
    }
}

// field access helpers for generated decoders: missing or mistyped
// fields yield zero values (Go's encoding/json behavior).
func FieldStr(m map<string, Value>, k string) string {
    return match m[k] {
    Str(x) -> x
    _ -> ""
    }
}

func FieldInt(m map<string, Value>, k string) int {
    return match m[k] {
    Num(f) -> int(f)
    _ -> 0
    }
}

func FieldBool(m map<string, Value>, k string) bool {
    return match m[k] {
    Bool(b) -> b
    _ -> false
    }
}

func FieldFloat(m map<string, Value>, k string) float64 {
    return match m[k] {
    Num(f) -> f
    _ -> 0.0
    }
}

func FieldObj(m map<string, Value>, k string) map<string, Value> {
    return match m[k] {
    Obj(o) -> o
    _ -> emptyObj()
    }
}

func FieldArr(m map<string, Value>, k string) [Value] {
    return match m[k] {
    Arr(a) -> a
    _ -> [Value]{}
    }
}

func emptyObj() map<string, Value> {
    var m map<string, Value>
    return m
}

func ValueStr(v Value) string {
    return match v {
    Str(x) -> x
    _ -> ""
    }
}

func ValueInt(v Value) int {
    return match v {
    Num(f) -> int(f)
    _ -> 0
    }
}

func ValueBool(v Value) bool {
    return match v {
    Bool(b) -> b
    _ -> false
    }
}

func ValueObj(v Value) map<string, Value> {
    return match v {
    Obj(o) -> o
    _ -> emptyObj()
    }
}
`

const jsonGo = `package json

import (
	"strconv"

	"goppout/gopp"
)

func Quote(s string) string { return strconv.Quote(s) }

func ParseF(s string) gopp.Result[float64, string] {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return gopp.Err[float64, string](err.Error())
	}
	return gopp.Ok[float64, string](f)
}

func BoolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func Float(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
`
