package main

import (
	"os"
	"testing"
)

func mustParse(t *testing.T, path string) *File {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	toks, err := lex(string(src))
	if err != nil {
		t.Fatalf("lex %s: %v", path, err)
	}
	f, diags := parse(toks)
	if diags.HasErrors() {
		t.Fatalf("parse %s:\n%s", path, diags)
	}
	return f
}

func TestParseHello(t *testing.T) {
	f := mustParse(t, "examples/hello.gopp")
	var enums, funcs int
	for _, d := range f.Decls {
		switch d.(type) {
		case *EnumDecl:
			enums++
		case *FuncDecl:
			funcs++
		}
	}
	if enums != 1 || funcs != 2 {
		t.Fatalf("hello.gopp: got %d enums, %d funcs; want 1, 2", enums, funcs)
	}
	// the enum should be Status with 3 variants, Failed has 1 field
	e := f.Decls[0].(*EnumDecl)
	if e.Name != "Status" || len(e.Variants) != 3 {
		t.Fatalf("enum: got %s with %d variants", e.Name, len(e.Variants))
	}
	if e.Variants[2].Name != "Failed" || len(e.Variants[2].Fields) != 1 {
		t.Fatalf("Failed variant: %+v", e.Variants[2])
	}
	if e.Variants[2].Fields[0].Name != "reason" {
		t.Fatalf("Failed field name: %q", e.Variants[2].Fields[0].Name)
	}
}

func TestParseFeatures(t *testing.T) {
	f := mustParse(t, "examples/features.gopp")
	var main *FuncDecl
	for _, d := range f.Decls {
		if fn, ok := d.(*FuncDecl); ok && fn.Name == "main" {
			main = fn
		}
	}
	if main == nil {
		t.Fatal("no main func")
	}
	// count match expressions and loop statements in main
	var matches, loops, vars, assignMatches int
	for _, s := range main.Body.List {
		switch st := s.(type) {
		case *ExprStmt:
			if _, ok := st.X.(*MatchExpr); ok {
				matches++
			}
		case *LoopStmt:
			loops++
		case *VarStmt:
			vars++
		case *AssignStmt:
			for _, rhs := range st.Rhs {
				if _, ok := rhs.(*MatchExpr); ok {
					assignMatches++
				}
			}
		}
	}
	if matches != 4 || assignMatches != 1 {
		t.Fatalf("got %d statement matches, %d assign matches; want 4, 1", matches, assignMatches)
	}
	if loops != 1 || vars != 1 {
		t.Fatalf("got %d loops, %d var stmts; want 1, 1", loops, vars)
	}
}

func TestParseArmShapes(t *testing.T) {
	src := `package main
func f() {
    match {
    v := ch.recv() -> use(v)
    ch.send(x) -> sent()
    after(1 * second) -> tick()
    _ -> done()
    }
}
`
	toks, err := lex(src)
	if err != nil {
		t.Fatal(err)
	}
	f, diags := parse(toks)
	if diags.HasErrors() {
		t.Fatal(diags)
	}
	fn := f.Decls[0].(*FuncDecl)
	m := fn.Body.List[0].(*ExprStmt).X.(*MatchExpr)
	kinds := []string{}
	for _, a := range m.Arms {
		switch a.Pat.(type) {
		case *RecvPat:
			kinds = append(kinds, "recv")
		case *SendPat:
			kinds = append(kinds, "send")
		case *AfterPat:
			kinds = append(kinds, "after")
		case *WildcardPat:
			kinds = append(kinds, "wild")
		}
	}
	want := []string{"recv", "send", "after", "wild"}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("arm %d: got %v, want %v", i, kinds, want)
		}
	}
}
