package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// gopp is the go++ compiler: it compiles a single .gopp source file into a
// runnable Go package (go++ -> Go -> binary via the Go toolchain or a
// TinyGo fork).
//
// Pipeline: lex (lex.go) -> parse (parse.go) -> check (sema.go) -> emit
// (emit.go). This is a real frontend: name resolution, type checking with
// generic enum instantiation, and compile-time exhaustiveness checking.
//
// Language subset (v2):
//   - enum declarations, incl. generics: enum Result[T, E] { Ok(T) Err(E) }
//   - match on a subject (variants, literals, bindings, guards) with
//     compile-time exhaustiveness checking; match without a subject over
//     channel arms (recv/send/after/_) or boolean arms
//   - chan[T](cap) construction and .send/.recv/.close methods
//   - loop { } with break loop
//   - maps instantiated on declaration: var m map[K]V lowers to make(...)
//   - Result[T,E] / Option[T] from the emitted prelude
//
// Rejected with clear errors (not yet supported):
//   - guards on channel arms and .closed() arms (Go channels cannot peek)
//   - comptime, @derive, the ? try operator, imports, struct types
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
	file, err := parse(toks)
	if err != nil {
		fatal(err)
	}
	chk, err := check(file)
	if err != nil {
		fatal(err)
	}
	goSrc := emit(file, chk)
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
	fmt.Printf("compiled %s -> %s (cd %s && go run .)\n", in, outDir, outDir)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gopp:", err)
	os.Exit(1)
}
