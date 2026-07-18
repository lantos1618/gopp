package main

import (
	"os"
	"os/exec"
	"path/filepath"
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
	file, err := parse(toks)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	chk, diags := check(file)
	if diags.HasErrors() {
		t.Fatalf("check:\n%s", diags)
	}
	dir := t.TempDir()
	write := func(name, data string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", emit(file, chk))
	write("gopp_prelude.go", prelude)
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
