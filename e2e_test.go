package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// compileAndRun drives the full pipeline on a .gopp file and runs the
// resulting Go package, returning its combined output.
func compileAndRun(t *testing.T, srcPath string) string {
	t.Helper()
	src, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	toks, err := lex(string(src))
	if err != nil {
		t.Fatalf("lex: %v", err)
	}
	file, parseDiags := parse(toks)
	if parseDiags.HasErrors() {
		t.Fatalf("parse:\n%s", parseDiags)
	}
	chk, diags := checkImports(file, nil, nil, checkOpts{src: string(src)})
	if diags.HasErrors() {
		t.Fatalf("check:\n%s", diags)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "gopp"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, data string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", emit(file, chk))
	write("gopp/gopp.go", prelude)
	write("go.mod", "module goppout\n\ngo 1.23\n")
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run failed: %v\n%s", err, out)
	}
	return string(out)
}

func TestEndToEndHello(t *testing.T) {
	got := compileAndRun(t, "examples/hello.gopp")
	want := "live\nlive\nrecv fired\n5\nerr: division by zero\n"
	if got != want {
		t.Fatalf("hello.gopp output:\n got %q\nwant %q", got, want)
	}
}

func TestEndToEndFeatures(t *testing.T) {
	got := compileAndRun(t, "examples/features.gopp")
	want := "0\n1\n2\nbig\ngot 42\nwarm\nzero\nmany\nmedium\ntimeout\n90\n"
	if got != want {
		t.Fatalf("features.gopp output:\n got %q\nwant %q", got, want)
	}
}

func TestEndToEndStructs(t *testing.T) {
	got := compileAndRun(t, "examples/structs.gopp")
	want := "25\n2 gopher\n0 0\n"
	if got != want {
		t.Fatalf("structs.gopp output:\n got %q\nwant %q", got, want)
	}
}

func TestEndToEndTry(t *testing.T) {
	got := compileAndRun(t, "examples/try.gopp")
	want := "ok\nerr: negative id\nerr: too many\ndirect: 61\n"
	if got != want {
		t.Fatalf("try.gopp output:\n got %q\nwant %q", got, want)
	}
}

func TestEndToEndComptime(t *testing.T) {
	got := compileAndRun(t, "examples/comptime.gopp")
	want := "7\n1048576\ngo++\ntrue\n42\n2\n3000\n60ns\nabc\n"
	if got != want {
		t.Fatalf("comptime.gopp output:\n got %q\nwant %q", got, want)
	}
}

// compilePkgAndRun drives the multi-package pipeline (§3) on a directory
// and runs the resulting Go module.
func compilePkgAndRun(t *testing.T, dir string) string {
	t.Helper()
	root := loadGraph(dir)
	checkGraph(root)
	if graphHasErrors(root) {
		for _, p := range topoOrder(root) {
			if len(p.diags.items) > 0 {
				t.Logf("# %s\n%s", p.dir, p.diags.Render(p.src))
			}
		}
		t.Fatal("checkGraph failed")
	}
	out := t.TempDir()
	emitGraph(root, out)
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = out
	out2, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run failed: %v\n%s", err, out2)
	}
	return string(out2)
}

func TestEndToEndImports(t *testing.T) {
	got := compilePkgAndRun(t, "examples/imports")
	want := "3 -2\nfourth\n0 0\nfourth\nbox 42\n"
	if got != want {
		t.Fatalf("imports output:\n got %q\nwant %q", got, want)
	}
}

func TestEndToEndMeta(t *testing.T) {
	got := compileAndRun(t, "examples/meta.gopp")
	want := "hi gopher\nneon\nada 36\nneon\nhappy\n55\n"
	if got != want {
		t.Fatalf("meta.gopp output:\n got %q\nwant %q", got, want)
	}
}

func TestEndToEndGeneric(t *testing.T) {
	got := compileAndRun(t, "examples/generic.gopp")
	want := "42\ngo++\ntrue\n1\n7\n9\none 1\n"
	if got != want {
		t.Fatalf("generic.gopp output:\n got %q\nwant %q", got, want)
	}
}

func TestEndToEndBehavior(t *testing.T) {
	got := compileAndRun(t, "examples/behavior.gopp")
	want := "active\nactive\nactive!\ninactive!\ncelsius!\n"
	if got != want {
		t.Fatalf("behavior.gopp output:\n got %q\nwant %q", got, want)
	}
}

func TestEndToEndOperators(t *testing.T) {
	got := compileAndRun(t, "examples/operators.gopp")
	want := "11 22\n9 18\n-1 -2\ntrue\ntrue\ntrue\ntrue\n11 22\n"
	if got != want {
		t.Fatalf("operators.gopp output:\n got %q\nwant %q", got, want)
	}
}

func TestEndToEndStdlib(t *testing.T) {
	got := compilePkgAndRun(t, "examples/stdlib")
	want := "GO++!\na-b-c\ntrue\ntrue\nbbb\n42!\n124\nerr\n4\n3\n2.5\n2.5\ntrue\nwrote true\nhi from go++\nread err\nslept\ntrue\n1 8\n8 1\ntrue\ntrue\n"
	if got != want {
		t.Fatalf("stdlib output:\n got %q\nwant %q", got, want)
	}
}

func TestEndToEndGenericImpl(t *testing.T) {
	got := compileAndRun(t, "examples/generic_impl.gopp")
	want := "full\nempty\nfull\n6 8\n"
	if got != want {
		t.Fatalf("generic_impl.gopp output:\n got %q\nwant %q", got, want)
	}
}

func TestEndToEndDefaultMethod(t *testing.T) {
	got := compileAndRun(t, "examples/default_method.gopp")
	want := "positive\nzero\nspecial\n"
	if got != want {
		t.Fatalf("default_method.gopp output:\n got %q\nwant %q", got, want)
	}
}

func TestEndToEndIndexOverload(t *testing.T) {
	got := compileAndRun(t, "examples/index_overload.gopp")
	want := "5\n9\n0\n6\n"
	if got != want {
		t.Fatalf("index_overload.gopp output:\n got %q\nwant %q", got, want)
	}
}

// writePkg makes a one-file package in dir/name for loader unit tests.
func writePkg(t *testing.T, dir, name, src string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func graphDiagMessages(root *pkg) string {
	var msgs string
	for _, p := range topoOrder(root) {
		for _, d := range p.diags.items {
			msgs += d.Msg + "\n"
		}
	}
	return msgs
}

func TestImportCycle(t *testing.T) {
	dir := t.TempDir()
	// a imports its subdirectory b; b imports ".." back to a
	writePkg(t, filepath.Join(dir, "a"), "a.gopp", "package a\n\nimport \"b\"\n\nfunc A() int { return 1 }\n")
	writePkg(t, filepath.Join(dir, "a", "b"), "b.gopp", "package b\n\nimport \"..\"\n\nfunc B() int { return 1 }\n")
	root := loadGraph(filepath.Join(dir, "a"))
	checkGraph(root)
	if got := graphDiagMessages(root); !strings.Contains(got, "import cycle") {
		t.Fatalf("expected import cycle error, got:\n%s", got)
	}
}

func TestImportUnexported(t *testing.T) {
	dir := t.TempDir()
	writePkg(t, dir, "main.gopp", "package main\n\nimport \"foo\"\n\nfunc main() { println(foo.hidden()) }\n")
	writePkg(t, filepath.Join(dir, "foo"), "foo.gopp", "package foo\n\nfunc hidden() int { return 1 }\n")
	root := loadGraph(dir)
	checkGraph(root)
	if got := graphDiagMessages(root); !strings.Contains(got, "not exported") {
		t.Fatalf("expected not-exported error, got:\n%s", got)
	}
}

func TestImportUnknownName(t *testing.T) {
	dir := t.TempDir()
	writePkg(t, dir, "main.gopp", "package main\n\nimport \"foo\"\n\nfunc main() { println(foo.Nope()) }\n")
	writePkg(t, filepath.Join(dir, "foo"), "foo.gopp", "package foo\n\nfunc Yes() int { return 1 }\n")
	root := loadGraph(dir)
	checkGraph(root)
	if got := graphDiagMessages(root); !strings.Contains(got, "undefined: foo.Nope") {
		t.Fatalf("expected undefined error, got:\n%s", got)
	}
}

func TestImportQualifierShadowed(t *testing.T) {
	// a local variable wins over the package qualifier (§3, like Go)
	dir := t.TempDir()
	writePkg(t, dir, "main.gopp", `package main

import "foo"

type Box struct {
    N int
}

func main() {
    foo := Box{N: 7}
    println(foo.N)
    println(foo.Yes())
}
`)
	writePkg(t, filepath.Join(dir, "foo"), "foo.gopp", "package foo\n\nfunc Yes() int { return 1 }\n")
	root := loadGraph(dir)
	checkGraph(root)
	// foo.N resolves as a field (shadowing), foo.Yes() then errors as a
	// non-callable field — the point is the qualifier did NOT win
	if got := graphDiagMessages(root); strings.Contains(got, "undefined: foo") {
		t.Fatalf("qualifier should have been shadowed, got:\n%s", got)
	}
}
