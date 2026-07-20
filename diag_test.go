package main

import "testing"

// diag_test.go — golden tests for the human-facing diagnostic rendering
// (§11): snippets, carets, secondary labels, suggestions.

func TestRenderGolden(t *testing.T) {
	src := `package main
func add(a int, b int) int {
    return "nope"
}
func main() {
    x := 1
    x := 2
    println(adm)
}
`
	want := `line 3: error: expected int, found string
  3 |     return "nope"
  = note: because of the return type declared here (line 2)
  2 | func add(a int, b int) int {
line 7: error: x redeclared in this scope
  7 |     x := 2
  = note: previous declaration of x here (line 6)
  6 |     x := 1
line 8: error: undefined: adm
  8 |     println(adm)
  = note: did you mean add?
`
	if got := runUIFile(src).Render(src); got != want {
		t.Fatalf("Render mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderCaretGolden(t *testing.T) {
	src := "package main\nfunc main() {\n    x := <-ch\n}\n"
	want := `line 3:10: error: <- was removed in go++ — use ch.recv() and ch.send(v)
  3 |     x := <-ch
    |          ^
`
	if got := runUIFile(src).Render(src); got != want {
		t.Fatalf("Render mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}
