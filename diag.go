package main

import (
	"fmt"
	"sort"
	"strings"
)

// diag.go — diagnostics infrastructure (skeleton §0/§11).
// The compiler never stops at the first error: every pass records
// diagnostics and keeps going, returning partial results. Type errors
// poison with tError (sema.go), which unifies silently, so one mistake
// produces one diagnostic instead of a cascade.

type Severity int

const (
	sevErr Severity = iota
	sevWarn
)

func (s Severity) String() string {
	if s == sevWarn {
		return "warning"
	}
	return "error"
}

// Diagnostic is a single compiler diagnostic. Line and Col are 1-based;
// 0 means "not attached to a source position".
type Diagnostic struct {
	Line  int
	Col   int
	Sev   Severity
	Msg   string
	Notes []Note
}

// Note is a secondary label (§11): it points at the code that EXPLAINS
// the primary diagnostic — "expected because of this", "declared here".
type Note struct {
	Line, Col int
	Msg       string
}

// note attaches a secondary label to the diagnostic.
func (d *Diagnostic) note(line, col int, msg string) {
	d.Notes = append(d.Notes, Note{Line: line, Col: col, Msg: msg})
}

func (d Diagnostic) String() string {
	if d.Line > 0 {
		return fmt.Sprintf("line %d: %s: %s", d.Line, d.Sev, d.Msg)
	}
	return fmt.Sprintf("%s: %s", d.Sev, d.Msg)
}

// Diagnostics collects all diagnostics from all passes.
type Diagnostics struct {
	items []Diagnostic
}

func (d *Diagnostics) errorf(line int, format string, args ...any) {
	d.errorfAt(line, 0, format, args...)
}

// errorfAt records an error with a column and returns it so the caller
// can attach secondary labels.
func (d *Diagnostics) errorfAt(line, col int, format string, args ...any) *Diagnostic {
	d.items = append(d.items, Diagnostic{Line: line, Col: col, Sev: sevErr, Msg: fmt.Sprintf(format, args...)})
	return &d.items[len(d.items)-1]
}

func (d *Diagnostics) warnf(line int, format string, args ...any) {
	d.items = append(d.items, Diagnostic{Line: line, Sev: sevWarn, Msg: fmt.Sprintf(format, args...)})
}

// add records an externally-produced error (lex/parse) as a diagnostic.
func (d *Diagnostics) add(sev Severity, line int, msg string) {
	d.items = append(d.items, Diagnostic{Line: line, Sev: sev, Msg: msg})
}

func (d *Diagnostics) HasErrors() bool {
	for _, it := range d.items {
		if it.Sev == sevErr {
			return true
		}
	}
	return false
}

// sorted returns diagnostics in source order (stable for equal lines).
func (d *Diagnostics) sorted() []Diagnostic {
	out := make([]Diagnostic, len(d.items))
	copy(out, d.items)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Sev < out[j].Sev
	})
	return out
}

func (d *Diagnostics) String() string {
	var b strings.Builder
	for _, it := range d.sorted() {
		b.WriteString(it.String())
		b.WriteByte('\n')
	}
	return b.String()
}

// Render formats diagnostics for humans (§11): a greppable
// "line:col: error:" header, the source line with a caret under the
// column, and secondary labels with snippets of their own.
func (d *Diagnostics) Render(src string) string {
	lines := strings.Split(src, "\n")
	var b strings.Builder
	for _, it := range d.sorted() {
		renderOne(&b, it, lines)
	}
	return b.String()
}

func renderOne(b *strings.Builder, d Diagnostic, lines []string) {
	if d.Line > 0 && d.Col > 0 {
		fmt.Fprintf(b, "line %d:%d: %s: %s\n", d.Line, d.Col, d.Sev, d.Msg)
	} else {
		b.WriteString(d.String())
		b.WriteByte('\n')
	}
	renderSnippet(b, d.Line, d.Col, lines)
	for _, n := range d.Notes {
		if n.Line > 0 {
			fmt.Fprintf(b, "  = note: %s (line %d)\n", n.Msg, n.Line)
		} else {
			fmt.Fprintf(b, "  = note: %s\n", n.Msg)
		}
		renderSnippet(b, n.Line, n.Col, lines)
	}
}

func renderSnippet(b *strings.Builder, line, col int, lines []string) {
	if line <= 0 || line > len(lines) {
		return
	}
	src := strings.ReplaceAll(lines[line-1], "\t", "    ")
	fmt.Fprintf(b, "  %d | %s\n", line, src)
	if col > 0 {
		// caret under the column: gutter + " | " + col-1 spaces
		b.WriteString("  " + strings.Repeat(" ", len(fmt.Sprint(line))) + " | " +
			strings.Repeat(" ", col-1) + "^\n")
	}
}
