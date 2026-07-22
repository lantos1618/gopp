package main

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ui_test.go — the §12 test harness: annotated negative tests.
//
// Files in tests/ui/*.gopp annotate expected diagnostics inline:
//
//	x := nope        //~ ERROR undefined: nope
//	return 1
//	println("dead")  //~ WARN unreachable code
//
// An annotation applies to the line it sits on. Every annotation must
// match an actual diagnostic on that line (severity + substring), and
// every actual diagnostic must be matched by an annotation — so both
// missing and unexpected diagnostics fail the test.

type uiWant struct {
	sev    Severity
	substr string
}

func parseUIAnnotations(t *testing.T, src string) map[int][]uiWant {
	t.Helper()
	wants := map[int][]uiWant{}
	for i, line := range strings.Split(src, "\n") {
		idx := strings.Index(line, "//~")
		if idx < 0 {
			continue
		}
		note := strings.TrimSpace(line[idx+3:])
		var w uiWant
		switch {
		case strings.HasPrefix(note, "ERROR "):
			w = uiWant{sevErr, strings.TrimSpace(note[6:])}
		case strings.HasPrefix(note, "WARN "):
			w = uiWant{sevWarn, strings.TrimSpace(note[5:])}
		default:
			t.Fatalf("line %d: malformed annotation (want //~ ERROR|WARN msg): %s", i+1, note)
		}
		wants[i+1] = append(wants[i+1], w)
	}
	return wants
}

// runUIFile drives the full pipeline on src and returns every diagnostic.
func runUIFile(src string) *Diagnostics {
	diags := &Diagnostics{}
	toks, err := lex(src)
	if err != nil {
		diagFromError(diags, err)
		return diags
	}
	f, parseDiags := parse(toks)
	diags.items = append(diags.items, parseDiags.items...)
	if diags.HasErrors() {
		return diags
	}
	_, sd := checkImports(f, nil, nil, checkOpts{src: src, srcDir: "tests/ui"})
	diags.items = append(diags.items, sd.items...)
	return diags
}

func TestUI(t *testing.T) {
	files, err := filepath.Glob("tests/ui/*.gopp")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no UI tests found in tests/ui/")
	}
	for _, path := range files {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			src := string(raw)
			wants := parseUIAnnotations(t, src)
			actual := runUIFile(src).sorted()

			matched := make([]bool, len(actual))
			var failures []string

			// every annotation must match a diagnostic on its line
			for line, ws := range wants {
				for _, w := range ws {
					found := false
					for i, d := range actual {
						if !matched[i] && d.Line == line && d.Sev == w.sev && strings.Contains(d.Msg, w.substr) {
							matched[i] = true
							found = true
							break
						}
					}
					if !found {
						failures = append(failures, fmt.Sprintf("line %d: expected %s %q, got none", line, w.sev, w.substr))
					}
				}
			}
			// every diagnostic must be matched by an annotation
			for i, d := range actual {
				if !matched[i] {
					failures = append(failures, fmt.Sprintf("unexpected diagnostic: %s", d))
				}
			}
			if len(failures) > 0 {
				t.Fatalf("%s:\n  %s\n\nfull output:\n%s", path, strings.Join(failures, "\n  "), actualString(actual))
			}
		})
	}
}

func actualString(ds []Diagnostic) string {
	var b strings.Builder
	for _, d := range ds {
		b.WriteString("  " + d.String() + "\n")
	}
	return b.String()
}

// TestFuzzNoCrash throws random byte soup and mutated valid programs at
// the pipeline (§12): the compiler must only ever diagnose, never panic.
func TestFuzzNoCrash(t *testing.T) {
	seeds := []string{}
	for _, p := range []string{"examples/hello.gopp", "examples/features.gopp"} {
		if raw, err := os.ReadFile(p); err == nil {
			seeds = append(seeds, string(raw))
		}
	}
	rng := rand.New(rand.NewSource(1))
	corpus := []string{"", "package main", "ë", "\x00\x01"}
	// random byte soup
	for i := 0; i < 200; i++ {
		n := rng.Intn(120)
		b := make([]byte, n)
		for j := range b {
			b[j] = byte(rng.Intn(256))
		}
		corpus = append(corpus, string(b))
	}
	// mutated valid programs: delete/swap/duplicate random chunks
	for _, s := range seeds {
		for i := 0; i < 100; i++ {
			corpus = append(corpus, mutate(rng, s))
		}
	}
	for i, src := range corpus {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("corpus[%d] panicked the compiler: %v\nsource:\n%s", i, r, src)
				}
			}()
			runUIFile(src)
		}()
	}
}

func mutate(rng *rand.Rand, s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	switch rng.Intn(3) {
	case 0: // delete a chunk
		i := rng.Intn(len(b))
		j := min(len(b), i+1+rng.Intn(16))
		return string(b[:i]) + string(b[j:])
	case 1: // swap two chunks
		i, j := rng.Intn(len(b)), rng.Intn(len(b))
		b[i], b[j] = b[j], b[i]
		return string(b)
	default: // duplicate a chunk
		i := rng.Intn(len(b))
		j := min(len(b), i+1+rng.Intn(16))
		return string(b[:j]) + string(b[i:j]) + string(b[j:])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
