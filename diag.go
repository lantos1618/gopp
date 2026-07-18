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

// Diagnostic is a single compiler diagnostic. Line is 1-based; 0 means
// "not attached to a source line".
type Diagnostic struct {
	Line int
	Sev  Severity
	Msg  string
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
	d.items = append(d.items, Diagnostic{Line: line, Sev: sevErr, Msg: fmt.Sprintf(format, args...)})
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
