package main

import (
	"testing"
)

// Positive sema tests live here; negative tests (expected diagnostics)
// live in tests/ui/*.gopp and run through TestUI (§12).

func mustCheck(t *testing.T, path string) {
	t.Helper()
	f := mustParse(t, path)
	if _, diags := check(f); diags.HasErrors() {
		t.Fatalf("check %s:\n%s", path, diags)
	}
}

func TestCheckExamples(t *testing.T) {
	mustCheck(t, "examples/hello.gopp")
	mustCheck(t, "examples/features.gopp")
}

func TestSemaWildcardSilencesExhaustiveness(t *testing.T) {
	toks, _ := lex(`package main
enum Status {
    Pending
    Failed(reason string)
}
func main() {
    s := Pending
    match s {
    Pending -> println("waiting")
    _ -> println("other")
    }
}
`)
	f, err := parse(toks)
	if err != nil {
		t.Fatal(err)
	}
	if _, diags := check(f); diags.HasErrors() {
		t.Fatalf("unexpected error:\n%s", diags)
	}
}

func TestSemaResultBindingTypes(t *testing.T) {
	// v binds int (Ok's payload), e binds string (Err's payload): both
	// usages below are well-typed, so this must NOT error.
	src := `package main
func f() Result[int, string] {
    return Ok[int, string](1)
}
func main() {
    r := f()
    match r {
    Ok(v) -> println(v + 1)
    Err(e) -> println(e + "!")
    }
}
`
	toks, _ := lex(src)
	f, err := parse(toks)
	if err != nil {
		t.Fatal(err)
	}
	if _, diags := check(f); diags.HasErrors() {
		t.Fatalf("unexpected error:\n%s", diags)
	}
}
