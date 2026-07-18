package main

import (
	"strings"
	"testing"
)

func mustCheck(t *testing.T, path string) {
	t.Helper()
	f := mustParse(t, path)
	if _, err := check(f); err != nil {
		t.Fatalf("check %s: %v", path, err)
	}
}

func TestCheckExamples(t *testing.T) {
	mustCheck(t, "examples/hello.gopp")
	mustCheck(t, "examples/features.gopp")
}

func wantErr(t *testing.T, src, want string) {
	t.Helper()
	toks, err := lex(src)
	if err != nil {
		t.Fatalf("lex: %v", err)
	}
	f, err := parse(toks)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, err = check(f)
	if err == nil {
		t.Fatalf("expected error containing %q, got none", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error containing %q, got %q", want, err.Error())
	}
}

func TestSemaExhaustiveness(t *testing.T) {
	wantErr(t, `package main
enum Status {
    Pending
    Active
    Failed(reason string)
}
func main() {
    s := Active
    match s {
    Pending -> println("waiting")
    Active -> println("live")
    }
}
`, "non-exhaustive match on Status: missing Failed")
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
	if _, err := check(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSemaUndefinedVar(t *testing.T) {
	wantErr(t, `package main
func main() {
    println(nope)
}
`, "undefined: nope")
}

func TestSemaArgTypeMismatch(t *testing.T) {
	wantErr(t, `package main
func add(a int, b int) int {
    return a + b
}
func main() {
    println(add(1, "two"))
}
`, "cannot use string as int")
}

func TestSemaChannelArmGuardRejected(t *testing.T) {
	wantErr(t, `package main
func main() {
    ch := chan[int](1)
    match {
    v := ch.recv() if v > 0 -> println(v)
    }
}
`, "guards on channel arms")
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
	if _, err := check(f); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSemaGenericCtorNeedsArgs(t *testing.T) {
	wantErr(t, `package main
func main() {
    r := Ok(1)
    _ = r
}
`, "generic")
}

func TestSemaBreakLoopOutside(t *testing.T) {
	wantErr(t, `package main
func main() {
    break loop
}
`, "break loop outside")
}
