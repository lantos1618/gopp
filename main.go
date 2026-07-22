package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
//   - chan<T>(cap) construction and .send/.recv/.close methods
//   - loop { } with break loop
//   - maps instantiated on declaration: var m map<K, V> lowers to make(...)
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
	if wasmMode {
		// js/wasm build: the goppCompile global is registered by an
		// init in wasm_js.go; keep the module alive instead of
		// parsing CLI args (there are none in the browser).
		select {}
	}
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gopp <input.gopp> [-o outdir] | gopp run <input.gopp> | gopp build <input.gopp> [-o binary] | gopp test [dir] | gopp fmt [-w] <files...> | gopp lsp | gopp play")
		os.Exit(2)
	}
	if os.Args[1] == "fmt" {
		runFmt(os.Args[2:])
		return
	}
	if os.Args[1] == "lsp" {
		// language server over stdio (§28)
		if err := serveLSP(os.Stdin, os.Stdout); err != nil {
			fatal(err)
		}
		return
	}
	if os.Args[1] == "run" {
		os.Exit(runRun(os.Args[2:]))
	}
	if os.Args[1] == "build" {
		os.Exit(runBuild(os.Args[2:]))
	}
	if os.Args[1] == "test" {
		os.Exit(runTest(os.Args[2:], os.Stdout, os.Stderr))
	}
	if os.Args[1] == "play" {
		// browser playground: wasm compiler + static file server
		os.Exit(runPlay(os.Args[2:]))
	}
	in := os.Args[1]
	outDir := "gopp-out"
	if len(os.Args) >= 4 && os.Args[2] == "-o" {
		outDir = os.Args[3]
	}
	if code := compile(in, outDir); code != 0 {
		os.Exit(code)
	}
	fmt.Printf("compiled %s -> %s (cd %s && go run .)\n", in, outDir, outDir)
}

// compile compiles in — a single file, or the entry of a directory-based
// package graph when it has imports (§3) — into a runnable Go module in
// outDir, and returns a process exit code (0 = success). All diagnostics
// from all passes are collected and printed together; codegen only runs
// on a clean bill (skeleton §0/§11).
func compile(in, outDir string) int {
	src, err := os.ReadFile(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gopp:", err)
		return 1
	}
	diags := &Diagnostics{}
	toks, err := lex(string(src))
	if err != nil {
		diagFromError(diags, err)
		printDiags(diags, string(src))
		return 1
	}
	file, parseDiags := parse(toks)
	diags.items = append(diags.items, parseDiags.items...)
	if diags.HasErrors() {
		// syntax errors: report them all, but don't run sema on a
		// partial AST — the follow-on noise helps nobody
		printDiags(diags, string(src))
		return 1
	}
	if len(file.Imports) > 0 {
		// directory mode (§3): the input's package is its whole directory
		root := loadGraph(filepath.Dir(in))
		checkGraph(root)
		if printGraphDiags(root) {
			return 1
		}
		emitGraph(root, outDir)
		return 0
	}
	chk, semDiags := checkImports(file, nil, nil, checkOpts{src: string(src), srcDir: filepath.Dir(in)})
	diags.items = append(diags.items, semDiags.items...)
	if printDiags(diags, string(src)) {
		return 1
	}
	goSrc := emit(file, chk)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "gopp:", err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(outDir, "main.go"), []byte(goSrc), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "gopp:", err)
		return 1
	}
	writePrelude(outDir)
	return 0
}

// runRun compiles the input to a temp dir and runs it with `go run .`,
// streaming the child's stdout/stderr through. Returns the child's exit
// code. Requires the go toolchain on PATH.
func runRun(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: gopp run <input.gopp>")
		return 2
	}
	if _, err := exec.LookPath("go"); err != nil {
		fmt.Fprintln(os.Stderr, "gopp run: go toolchain not found on PATH")
		return 1
	}
	outDir, err := os.MkdirTemp("", "gopp-run-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "gopp:", err)
		return 1
	}
	defer os.RemoveAll(outDir)
	if code := compile(args[0], outDir); code != 0 {
		return code
	}
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = outDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "gopp:", err)
		return 1
	}
	return 0
}

// runTest implements `gopp test <dir>`: load the package with its
// *_test.gopp files, generate the runner, and `go run` it.
func runTest(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: gopp test [dir | file.gopp]")
		return 2
	}
	dir := "."
	if len(args) == 1 {
		dir = args[0]
	}
	if strings.HasSuffix(dir, ".gopp") {
		dir = filepath.Dir(dir)
	}
	if _, err := exec.LookPath("go"); err != nil {
		fmt.Fprintln(stderr, "gopp test: go toolchain not found on PATH")
		return 1
	}
	root := loadGraphTests(dir)
	checkGraph(root)
	if printGraphDiags(root) {
		return 1
	}
	var tests []string
	for _, d := range root.file.Decls {
		if fn, ok := d.(*FuncDecl); ok && strings.HasPrefix(fn.Name, "Test") && len(fn.Params) == 0 && len(fn.Results) == 0 {
			tests = append(tests, fn.Name)
		}
	}
	if len(tests) == 0 {
		fmt.Fprintln(stderr, "gopp test: no Test functions found in", dir)
		return 1
	}
	outDir, err := os.MkdirTemp("", "gopp-test-")
	if err != nil {
		fmt.Fprintln(stderr, "gopp:", err)
		return 1
	}
	defer os.RemoveAll(outDir)
	emitGraphTest(root, outDir)
	if err := os.WriteFile(filepath.Join(outDir, "testmain.go"), []byte(testMainSrc(tests)), 0o644); err != nil {
		fmt.Fprintln(stderr, "gopp:", err)
		return 1
	}
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = outDir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(stderr, "gopp:", err)
		return 1
	}
	return 0
}

// testMainSrc renders the generated runner: each Test func runs under
// recover; failures report and exit non-zero.
func testMainSrc(tests []string) string {
	var b strings.Builder
	b.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\n")
	b.WriteString("func main() {\n")
	b.WriteString("tests := []struct {\nname string\nf func()\n}{\n")
	for _, t := range tests {
		fmt.Fprintf(&b, "{%q, %s},\n", t, t)
	}
	b.WriteString("}\n")
	b.WriteString(`failed := 0
for _, t := range tests {
func() {
defer func() {
if r := recover(); r != nil {
failed++
fmt.Printf("FAIL %s: %v\n", t.name, r)
}
}()
t.f()
fmt.Printf("ok   %s\n", t.name)
}()
}
if failed > 0 {
fmt.Printf("%d test(s) failed\n", failed)
os.Exit(1)
}
fmt.Printf("%d test(s) passed\n", len(tests))
}
`)
	return b.String()
}

// runBuild compiles the input to a real binary with `go build` (no
// module left behind). Returns the process exit code.
func runBuild(args []string) int {
	if len(args) != 1 && len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: gopp build <input.gopp> [-o binary]")
		return 2
	}
	if _, err := exec.LookPath("go"); err != nil {
		fmt.Fprintln(os.Stderr, "gopp build: go toolchain not found on PATH")
		return 1
	}
	out := ""
	if len(args) == 3 {
		if args[1] != "-o" {
			fmt.Fprintln(os.Stderr, "usage: gopp build <input.gopp> [-o binary]")
			return 2
		}
		out = args[2]
	} else {
		out = strings.TrimSuffix(filepath.Base(args[0]), filepath.Ext(args[0]))
	}
	outDir, err := os.MkdirTemp("", "gopp-build-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "gopp:", err)
		return 1
	}
	defer os.RemoveAll(outDir)
	if code := compile(args[0], outDir); code != 0 {
		return code
	}
	tmpBin := filepath.Join(outDir, "bin")
	cmd := exec.Command("go", "build", "-o", tmpBin, ".")
	cmd.Dir = outDir
	if out2, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "%s", out2)
		return 1
	}
	// rename is cheap on the same filesystem; fall back to a copy
	if err := os.Rename(tmpBin, out); err != nil {
		data, rerr := os.ReadFile(tmpBin)
		if rerr != nil {
			fmt.Fprintln(os.Stderr, "gopp:", rerr)
			return 1
		}
		if werr := os.WriteFile(out, data, 0o755); werr != nil {
			fmt.Fprintln(os.Stderr, "gopp:", werr)
			return 1
		}
	}
	fmt.Printf("built %s -> %s\n", args[0], out)
	return 0
}

// printDiags prints all collected diagnostics (with source snippets,
// §11) and reports whether any errors were recorded.
func printDiags(diags *Diagnostics, src string) bool {
	if len(diags.items) == 0 {
		return false
	}
	fmt.Fprint(os.Stderr, diags.Render(src))
	return diags.HasErrors()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gopp:", err)
	os.Exit(1)
}
