package main

// meta_test.go — §10 comptime metaprogramming tests: the walk output,
// declaration mutation, and .body source access.

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// checkMeta runs the pipeline on src with comptime print captured.
func checkMeta(t *testing.T, src string) (string, *Diagnostics) {
	t.Helper()
	var buf bytes.Buffer
	old := metaOut
	metaOut = &buf
	defer func() { metaOut = old }()
	toks, err := lex(src)
	if err != nil {
		t.Fatalf("lex: %v", err)
	}
	f, pd := parse(toks)
	if pd.HasErrors() {
		t.Fatalf("parse:\n%s", pd)
	}
	_, diags := checkImports(f, nil, nil, src)
	return buf.String(), diags
}

func TestComptimeWalkAndMutate(t *testing.T) {
	src, err := os.ReadFile("examples/meta.gopp")
	if err != nil {
		t.Fatal(err)
	}
	out, diags := checkMeta(t, string(src))
	if diags.HasErrors() {
		t.Fatalf("check:\n%s", diags.Render(string(src)))
	}
	want := "walking 6 decls\nenum Color\nstruct User\nfunc greet\nfunc fib\nfunc describe\nfunc main\nfib(10) = 55\n"
	if out != want {
		t.Fatalf("comptime output:\n got %q\nwant %q", out, want)
	}
}

func TestComptimeBodyText(t *testing.T) {
	src := `package main

func answer() int {
    return 42
}

comptime {
    for d in decls() {
        if d.name == "answer" {
            print(d.body)
        }
    }
}

func main() { println(answer()) }
`
	out, diags := checkMeta(t, src)
	if diags.HasErrors() {
		t.Fatalf("check:\n%s", diags.Render(src))
	}
	if !strings.Contains(out, "return 42") {
		t.Fatalf("expected body text in comptime output, got %q", out)
	}
}

func TestComptimeGenFunc(t *testing.T) {
	// build a whole function from nothing and call it
	src := `package main

comptime {
    f := Func("made")
    f.body = "println(\"from gen\")"
    gen(f)
    print("built", f.name)
}

func main() { made() }
`
	out, diags := checkMeta(t, src)
	if diags.HasErrors() {
		t.Fatalf("check:\n%s", diags.Render(src))
	}
	if !strings.Contains(out, "built made") {
		t.Fatalf("expected constructor output, got %q", out)
	}
}

func TestComptimeUseDeclared(t *testing.T) {
	// bare type handles, comptime calls of declared functions with loops
	// and recursion, and variables shared across comptime blocks
	src := `package main

enum Suit {
    Hearts
    Spades
}

func sumTo(n int) int {
    total := 0
    for i := 1; i <= n; i++ {
        total += i
    }
    return total
}

comptime {
    x := sumTo(100)
    s := Suit
    print("suit kind:", s.kind)
}

comptime {
    print("sumTo(100) =", x)
    f := Func("answer")
    f.results.add(Param("", "int"))
    f.body = "return " + str(x)
    gen(f)
}

func main() { println(answer()) }
`
	out, diags := checkMeta(t, src)
	if diags.HasErrors() {
		t.Fatalf("check:\n%s", diags.Render(src))
	}
	want := "suit kind: enum\nsumTo(100) = 5050\n"
	if out != want {
		t.Fatalf("comptime output:\n got %q\nwant %q", out, want)
	}
}

func TestComptimeMatch(t *testing.T) {
	// literal arms with guards and a binding arm, over an int subject
	src := `package main

comptime {
    d := match 3 {
        0 -> "zero"
        1 -> "one"
        n if n < 0 -> "neg"
        n if n > 2 -> "big " + str(n)
        _ -> "small"
    }
    print(d)
    s := match "hi" {
        "bye" -> 1
        "hi" -> 2
        _ -> 3
    }
    print(s)
}

func main() {}
`
	out, diags := checkMeta(t, src)
	if diags.HasErrors() {
		t.Fatalf("check:\n%s", diags.Render(src))
	}
	if out != "big 3\n2\n" {
		t.Fatalf("comptime output:\n got %q\nwant %q", out, "big 3\n2\n")
	}
}

func TestComptimeMatchBool(t *testing.T) {
	// subject-less match: first true bool arm wins
	src := `package main

comptime {
    x := 5
    y := match {
        if x > 10 -> "big"
        if x > 3 -> "mid"
        _ -> "small"
    }
    print(y)
}

func main() {}
`
	out, diags := checkMeta(t, src)
	if diags.HasErrors() {
		t.Fatalf("check:\n%s", diags.Render(src))
	}
	if out != "mid\n" {
		t.Fatalf("comptime output: got %q, want %q", out, "mid\n")
	}
}

func TestComptimeStringBuiltins(t *testing.T) {
	src := `package main

comptime {
    parts := split("alpha,beta,gamma", ",")
    print(len(parts))
    print(join(parts, "-"))
    print(upper("abc"), lower("DeF"), trim("  hi  "))
    print(replace("a-b-a", "a", "x"))
    print(contains("hello", "ell"), has_prefix("hello", "he"), has_suffix("hello", "lo"))
    print(repeat("ab", 3))
}

func main() {}
`
	out, diags := checkMeta(t, src)
	if diags.HasErrors() {
		t.Fatalf("check:\n%s", diags.Render(src))
	}
	want := "3\nalpha-beta-gamma\nABC def hi\nx-b-x\ntrue true true\nababab\n"
	if out != want {
		t.Fatalf("comptime output:\n got %q\nwant %q", out, want)
	}
}

func TestComptimeCodegenWithStrings(t *testing.T) {
	// build func names from pieces and gen them, then call them from main
	src := `package main

comptime {
    for n in split("get,set", ",") {
        f := Func(n + upper(n))
        f.body = "println(\"" + n + "\")"
        gen(f)
    }
}

func main() {
    getGET()
    setSET()
}
`
	_, diags := checkMeta(t, src)
	if diags.HasErrors() {
		t.Fatalf("check:\n%s", diags.Render(src))
	}
}

func TestComptimeMatchErrors(t *testing.T) {
	// variant patterns have no meaning at comptime
	_, diags := checkMeta(t, "package main\n\ncomptime {\n    x := match 1 {\n        Ok(v) -> v\n        _ -> 0\n    }\n}\n\nfunc main() {}\n")
	if !diags.HasErrors() || !strings.Contains(diags.String(), "variant patterns are not supported in comptime match") {
		t.Fatalf("expected variant-pattern error, got:\n%s", diags)
	}
	// no arm matching is a compile error, not a runtime panic
	_, diags = checkMeta(t, "package main\n\ncomptime {\n    x := match 5 {\n        0 -> \"zero\"\n    }\n}\n\nfunc main() {}\n")
	if !diags.HasErrors() || !strings.Contains(diags.String(), "non-exhaustive comptime match") {
		t.Fatalf("expected non-exhaustive error, got:\n%s", diags)
	}
	// statement bodies are rejected; arms must be expressions
	_, diags = checkMeta(t, "package main\n\ncomptime {\n    x := match 1 {\n        1 -> { print(\"hi\") }\n        _ -> 0\n    }\n}\n\nfunc main() {}\n")
	if !diags.HasErrors() || !strings.Contains(diags.String(), "comptime match arms must be expressions") {
		t.Fatalf("expected statement-body error, got:\n%s", diags)
	}
	// repeat is capped at 10000
	_, diags = checkMeta(t, "package main\n\ncomptime {\n    s := repeat(\"ab\", 10001)\n}\n\nfunc main() {}\n")
	if !diags.HasErrors() || !strings.Contains(diags.String(), "exceeds the 10000 limit") {
		t.Fatalf("expected repeat-cap error, got:\n%s", diags)
	}
}

func TestComptimeErrors(t *testing.T) {
	// unknown comptime builtin -> one clean diagnostic, no crash
	_, diags := checkMeta(t, "package main\n\ncomptime {\n    nosuch()\n}\n\nfunc main() {}\n")
	if !diags.HasErrors() || !strings.Contains(diags.String(), "undefined comptime function") {
		t.Fatalf("expected undefined-builtin error, got:\n%s", diags)
	}
	// mutating a name to a non-identifier is rejected
	_, diags = checkMeta(t, "package main\n\nfunc f() {}\n\ncomptime {\n    for d in decls() {\n        if d.name == \"f\" { d.name = \"not a name\" }\n    }\n}\n\nfunc main() {}\n")
	if !diags.HasErrors() || !strings.Contains(diags.String(), "identifier string") {
		t.Fatalf("expected name-validation error, got:\n%s", diags)
	}
}
