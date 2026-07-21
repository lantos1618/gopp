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
// Language subset (v3):
//   - enum declarations, incl. generics: enum Result[T, E] { Ok(T) Err(E) },
//     with type-argument inference from context (var r Result[int, string]
//     = Ok(1)) and struct types with field access, & and *
//   - match on a subject (variants, literals, bindings, guards) with
//     compile-time exhaustiveness checking; match without a subject over
//     channel arms (recv/send/after/_) or boolean arms
//   - chan[T](cap) construction and .send/.recv/.close methods
//   - loop { } with break loop
//   - maps instantiated on declaration: var m map[K]V lowers to make(...)
//   - Result[T,E] / Option[T] from the emitted prelude, with ? try
//   - comptime expr: constants folded at go++ compile time (§10)
//   - comptime metaprogramming: top-level comptime blocks walk and
//     rewrite the package's declarations before checking (§10)
//   - directory-based packages with import "dir" (§3): the qualifier is
//     the dependency's package name, capitalized = exported, cycles error
//   - strict numerics: untyped literal constants, explicit conversions,
//     no implicit width mixing; error/any/<- removed from the language
//
// Rejected with clear errors (not yet supported):
//   - guards on channel arms and .closed() arms (Go channels cannot peek)
//   - comptime functions
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gopp <input.gopp> [-o outdir] | gopp fmt [-w] <files...>")
		os.Exit(2)
	}
	if os.Args[1] == "fmt" {
		runFmt(os.Args[2:])
		return
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
	// All diagnostics from all passes are collected and printed together;
	// codegen only runs on a clean bill (skeleton §0/§11).
	diags := &Diagnostics{}
	toks, err := lex(string(src))
	if err != nil {
		diagFromError(diags, err)
		printDiags(diags, string(src))
	}
	file, parseDiags := parse(toks)
	diags.items = append(diags.items, parseDiags.items...)
	if diags.HasErrors() {
		// syntax errors: report them all, but don't run sema on a
		// partial AST — the follow-on noise helps nobody
		printDiags(diags, string(src))
	}
	if len(file.Imports) > 0 {
		// directory mode (§3): the input's package is its whole directory
		root := loadGraph(filepath.Dir(in))
		checkGraph(root)
		if printGraphDiags(root) {
			os.Exit(1)
		}
		emitGraph(root, outDir)
		fmt.Printf("compiled %s -> %s (cd %s && go run .)\n", in, outDir, outDir)
		return
	}
	chk, semDiags := checkImports(file, nil, nil, string(src))
	diags.items = append(diags.items, semDiags.items...)
	printDiags(diags, string(src))
	goSrc := emit(file, chk)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "main.go"), []byte(goSrc), 0o644); err != nil {
		fatal(err)
	}
	writePrelude(outDir)
	fmt.Printf("compiled %s -> %s (cd %s && go run .)\n", in, outDir, outDir)
}

// printDiags prints all collected diagnostics (with source snippets,
// §11) and exits non-zero if any errors were recorded.
func printDiags(diags *Diagnostics, src string) {
	if len(diags.items) == 0 {
		return
	}
	fmt.Fprint(os.Stderr, diags.Render(src))
	if diags.HasErrors() {
		os.Exit(1)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gopp:", err)
	os.Exit(1)
}
