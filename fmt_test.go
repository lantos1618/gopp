package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fmt_test.go — unit tests for the formatter's whitespace-hygiene rules
// (blank-line collapse, trailing-whitespace strip, single final newline,
// "} else" joining) plus idempotency on synthetic inputs and on every
// example under examples/ (which must already be canonical: reformatting
// them is a byte-for-byte no-op).

func TestFmtCollapsesBlankRuns(t *testing.T) {
	src := "package main\n\n\n\nfunc main() {\n    x := 1\n\n\n    println(x)\n}\n"
	want := "package main\n\nfunc main() {\n    x := 1\n\n    println(x)\n}\n"
	if got := formatSource(src); got != want {
		t.Fatalf("formatSource mismatch:\n got:\n%q\nwant:\n%q", got, want)
	}
}

func TestFmtStripsTrailingWhitespace(t *testing.T) {
	src := "package main  \nfunc main() {\t\n    x := 1 \t \n}\n"
	want := "package main\nfunc main() {\n    x := 1\n}\n"
	if got := formatSource(src); got != want {
		t.Fatalf("formatSource mismatch:\n got:\n%q\nwant:\n%q", got, want)
	}
}

func TestFmtFinalNewline(t *testing.T) {
	for _, src := range []string{
		"package main",            // no trailing newline at all
		"package main\n",          // already canonical
		"package main\n\n\n",      // extra trailing blank lines
		"package main\n   \n\t\n", // trailing whitespace-only lines
	} {
		if got := formatSource(src); got != "package main\n" {
			t.Fatalf("formatSource(%q) = %q, want %q", src, got, "package main\n")
		}
	}
}

func TestFmtJoinsElse(t *testing.T) {
	src := `func f(x int) {
    if x > 0 {
        println("pos")
    }
    else if x < 0 {
        println("neg")
    }
    else {
        println("zero")
    }
}
`
	want := `func f(x int) {
    if x > 0 {
        println("pos")
    } else if x < 0 {
        println("neg")
    } else {
        println("zero")
    }
}
`
	if got := formatSource(src); got != want {
		t.Fatalf("formatSource mismatch:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFmtElseNotJoinedAcrossComment(t *testing.T) {
	// A comment between } and else blocks the join; only reindentation
	// and hygiene may apply.
	src := `func f(x int) {
    if x > 0 {
        println("pos")
    }
    // fall through to zero
    else {
        println("zero")
    }
}
`
	if got := formatSource(src); got != src {
		t.Fatalf("formatSource changed input:\n got:\n%s\nwant unchanged:\n%s", got, src)
	}
	// A trailing comment on the } line blocks the join too.
	src2 := `func f(x int) {
    if x > 0 {
        println("pos")
    } // end pos
    else {
        println("zero")
    }
}
`
	if got := formatSource(src2); got != src2 {
		t.Fatalf("formatSource changed input:\n got:\n%s\nwant unchanged:\n%s", got, src2)
	}
}

func TestFmtIdempotent(t *testing.T) {
	inputs := []string{
		"",
		"\n\n\n",
		"package main",
		"package main\n\n\n\nfunc main() {\n    x := 1 \t\n\n\n    println(x)\n}\n\n\n",
		"func f() {\n    if true {\n    }\n    else {\n    }\n}\n",
		"func f() {\n    if true {\n    } // note\n    else {\n    }\n}\n",
		"func f() {\n    s := \"} else { not code\"\n    println(s)\n}\n",
		"/* block\n\n\n   comment */\npackage main\n",
		"func f() {\n    if true {\n    }\n    // c\n    else {\n    }\n}\n",
	}
	for _, src := range inputs {
		once := formatSource(src)
		if twice := formatSource(once); twice != once {
			t.Fatalf("not idempotent for %q:\n once: %q\n twice: %q", src, once, twice)
		}
	}
}

func TestFmtExamplesIdempotent(t *testing.T) {
	var files []string
	err := filepath.Walk("examples", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".gopp") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no example .gopp files found")
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if got := formatSource(string(data)); got != string(data) {
			t.Errorf("%s is not canonically formatted (run: gopp fmt -w %s)", f, f)
		}
	}
}
