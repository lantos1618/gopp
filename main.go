package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// gopp is the go++ v0.1 transpiler: it converts a single .gopp source file
// into a runnable Go package (go++ -> Go -> binary via the Go toolchain or
// a TinyGo fork).
//
// Supported v0.1 subset:
//   - enum declarations (unit + payload variants, non-generic)
//   - match on a subject (enum variants with destructuring, literals, _,
//     guards via `if`), as a statement or as a value-producing expression
//   - match without a subject over channel arms (recv/send/after/_), i.e.
//     the select replacement
//   - chan[T](cap) construction and .send/.recv/.close methods
//   - loop { } with break loop
//   - maps that are instantiated on declaration: var m map[K]V
//     lowers to var m map[K]V = make(map[K]V), no nil-map panics
//   - Result[T,E] / Option[T] from the emitted prelude
//
// Known v0.1 limitations (rejected with clear errors):
//   - guards on channel arms (needs peek semantics Go channels lack)
//   - .closed() match arms
//   - generic user enums, comptime, @derive, the ? try operator
//   - bare Ok(...)/Err(...) calls need explicit type args: Ok[int, string](v)
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gopp <input.gopp> [-o outdir]")
		os.Exit(2)
	}
	in := os.Args[1]
	outDir := "gopp-out"
	if len(os.Args) >= 4 && os.Args[2] == "-o" {
		outDir = os.Args[3]
	}
	src, err := os.ReadFile(in)
	if err != nil {
		fatal(err)
	}
	toks, err := lex(string(src))
	if err != nil {
		fatal(err)
	}
	goSrc, err := newTr().xform(toks)
	if err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatal(err)
	}
	w := func(name, data string) {
		if err := os.WriteFile(filepath.Join(outDir, name), []byte(data), 0o644); err != nil {
			fatal(err)
		}
	}
	w("main.go", goSrc)
	w("gopp_prelude.go", prelude)
	w("go.mod", "module goppout\n\ngo 1.23\n")
	fmt.Printf("transpiled %s -> %s (cd %s && go run .)\n", in, outDir, outDir)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gopp:", err)
	os.Exit(1)
}
