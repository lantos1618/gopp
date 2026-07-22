package main

// pkg.go — package loading and the multi-package driver (§3).
//
// A go++ package is a directory of .gopp files sharing one package clause
// and one namespace. `import "foo"` loads the subdirectory ./foo relative
// to the importing package; the qualifier in code is the dependency's
// package NAME (Go's rule), capitalized names are exported, import cycles
// are errors. Multi-file packages keep accurate diagnostics: each file is
// lexed at a line offset (lexAt) so positions match the concatenated
// package source.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type pkg struct {
	dir      string        // source directory (absolute)
	out      string        // output dir relative to the module root ("" = root)
	name     string        // package clause
	file     *File         // all files merged, line offsets applied
	src      string        // concatenated sources (diagnostics render against this)
	imports  []*ImportDecl // merged, deduped by path
	deps     []*pkg        // parallel to imports
	diags    *Diagnostics  // load/parse/sema diagnostics for this package
	chk      *checker      // set by checkGraph
	stdlibGo string        // embedded stdlib: native .go implementation (§FFI)
}

// loadGraph loads the package in dir and, recursively, its imports.
func loadGraph(dir string) *pkg {
	return loadPkg(dir, "", nil, map[string]*pkg{})
}

func loadPkg(dir string, impPath string, stack []string, cache map[string]*pkg) *pkg {
	abs, err := filepath.Abs(dir)
	if err != nil {
		p := &pkg{diags: &Diagnostics{}}
		p.diags.errorf(0, "%s", err)
		return p
	}
	if p, ok := cache[abs]; ok {
		return p
	}
	p := &pkg{dir: abs, diags: &Diagnostics{}}
	cache[abs] = p

	// embedded stdlib fallback: `import "str"` names no directory, so the
	// compiler's own registry provides the package (stdlib.go)
	if sp, ok := stdlibPackages[impPath]; ok {
		if _, derr := os.Stat(abs); derr != nil {
			toks, lerr := lexAt(sp.src, 0)
			if lerr != nil {
				diagFromError(p.diags, lerr)
				return p
			}
			f, parseDiags := parse(toks)
			p.diags.items = append(p.diags.items, parseDiags.items...)
			p.file = f
			p.name = f.PkgName
			p.src = sp.src
			p.stdlibGo = sp.impl
			return p
		}
	}

	var names []string
	if ents, err := os.ReadDir(abs); err != nil {
		p.diags.errorf(0, "%s", err)
		return p
	} else {
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".gopp") {
				names = append(names, e.Name())
			}
		}
	}
	sort.Strings(names) // deterministic merge order
	if len(names) == 0 {
		p.diags.errorf(0, "no .gopp files in %s", abs)
		return p
	}

	var srcs strings.Builder
	base := 0
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(abs, name))
		if err != nil {
			p.diags.errorf(0, "%s", err)
			continue
		}
		src := string(data)
		toks, lerr := lexAt(src, base)
		if lerr != nil {
			diagFromError(p.diags, lerr)
		} else {
			f, parseDiags := parse(toks)
			p.diags.items = append(p.diags.items, parseDiags.items...)
			if p.file == nil {
				p.file = f
				p.name = f.PkgName
			} else {
				if f.PkgName != p.name {
					p.diags.errorfAt(base+1, 0,
						"package clause mismatch: %s is %s, %s is %s",
						name, f.PkgName, names[0], p.name)
				}
				p.file.Imports = append(p.file.Imports, f.Imports...)
				p.file.Decls = append(p.file.Decls, f.Decls...)
			}
		}
		srcs.WriteString(src)
		srcs.WriteString("\n")
		base += strings.Count(src, "\n") + 1
	}
	p.src = srcs.String()
	if p.file == nil {
		return p
	}

	// load imports, deduped by path; qualifier = dependency package name
	seen := map[string]bool{}
	quals := map[string]int{} // qualifier -> first import line
	for _, imp := range p.file.Imports {
		if seen[imp.Path] {
			continue
		}
		seen[imp.Path] = true
		depDir := filepath.Join(abs, filepath.FromSlash(imp.Path))
		depAbs, err := filepath.Abs(depDir)
		if err == nil {
			if i := indexStr(stack, depAbs); i >= 0 {
				chain := append(append([]string{}, stack[i:]...), depAbs)
				var b []string
				for _, d := range chain {
					b = append(b, filepath.Base(d))
				}
				p.diags.errorf(imp.Line, "import cycle: %s", strings.Join(b, " -> "))
				continue
			}
			// everything a program imports must live under the root
			// package's directory, so emitted packages stay in the outdir
			rootDir := abs
			if len(stack) > 0 {
				rootDir = stack[0]
			}
			if rel, rerr := filepath.Rel(rootDir, depAbs); rerr != nil || strings.HasPrefix(rel, "..") {
				p.diags.errorf(imp.Line, "import %s escapes the root package directory", imp.Path)
				continue
			}
		}
		dep := loadPkg(depDir, imp.Path, append(append([]string{}, stack...), abs), cache)
		p.imports = append(p.imports, imp)
		p.deps = append(p.deps, dep)
		if dep.name == "" {
			continue
		}
		if prev, dup := quals[dep.name]; dup {
			p.diags.errorf(imp.Line, "import %s has the same package name %s as the import on line %d (aliases are not supported)", imp.Path, dep.name, prev)
		}
		quals[dep.name] = imp.Line
	}
	return p
}

func indexStr(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

// topoOrder lists the graph dependencies-first, each package once.
func topoOrder(root *pkg) []*pkg {
	var order []*pkg
	seen := map[*pkg]bool{}
	var visit func(p *pkg)
	visit = func(p *pkg) {
		if seen[p] {
			return
		}
		seen[p] = true
		for _, d := range p.deps {
			visit(d)
		}
		order = append(order, p)
	}
	visit(root)
	return order
}

// assignOut computes each package's output directory from the import
// paths, so the emitted Go import lines mirror the source tree.
func assignOut(root *pkg) {
	for _, p := range topoOrder(root) {
		for i, dep := range p.deps {
			dep.out = filepath.Join(p.out, filepath.FromSlash(p.imports[i].Path))
		}
	}
}

// checkGraph runs sema dependencies-first; each package's diagnostics
// (load, parse, sema) accumulate in pkg.diags.
func checkGraph(root *pkg) {
	assignOut(root)
	for _, p := range topoOrder(root) {
		if p.file == nil || p.diags.HasErrors() {
			continue // don't run sema on a broken parse (§0)
		}
		imports := map[string]*checker{}
		paths := map[string]string{}
		for i, dep := range p.deps {
			if dep.chk == nil {
				continue // broken dep: its own diagnostics say why
			}
			imports[dep.name] = dep.chk
			paths[dep.name] = filepath.ToSlash(filepath.Join(p.out, p.imports[i].Path))
		}
		chk, semDiags := checkImports(p.file, imports, paths, checkOpts{src: p.src, allowNative: p.stdlibGo != ""})
		p.chk = chk
		p.diags.items = append(p.diags.items, semDiags.items...)
	}
}

// graphHasErrors reports whether any package in the graph has errors.
func graphHasErrors(root *pkg) bool {
	for _, p := range topoOrder(root) {
		if p.diags.HasErrors() {
			return true
		}
	}
	return false
}

// printGraphDiags renders every package's diagnostics against its own
// concatenated source, dependencies-first. Returns true if any errors.
func printGraphDiags(root *pkg) bool {
	for _, p := range topoOrder(root) {
		if len(p.diags.items) == 0 {
			continue
		}
		fmt.Fprintf(os.Stderr, "# %s\n", p.dir)
		fmt.Fprint(os.Stderr, p.diags.Render(p.src))
	}
	return graphHasErrors(root)
}

// emitGraph writes one Go file per package into the outdir tree,
// plus the shared gopp prelude package and go.mod.
func emitGraph(root *pkg, outDir string) {
	for _, p := range topoOrder(root) {
		if p.chk == nil || p.diags.HasErrors() {
			continue
		}
		dir := filepath.Join(outDir, p.out)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fatal(err)
		}
		name := p.name + ".go"
		if p.name == "main" {
			name = "main.go"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(emit(p.file, p.chk)), 0o644); err != nil {
			fatal(err)
		}
		if p.stdlibGo != "" { // embedded stdlib's native implementation
			if err := os.WriteFile(filepath.Join(dir, "native.go"), []byte(p.stdlibGo), 0o644); err != nil {
				fatal(err)
			}
		}
	}
	writePrelude(outDir)
}

// writePrelude writes the shared runtime support package and go.mod.
func writePrelude(outDir string) {
	if err := os.MkdirAll(filepath.Join(outDir, "gopp"), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "gopp", "gopp.go"), []byte(prelude), 0o644); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "go.mod"), []byte("module goppout\n\ngo 1.23\n"), 0o644); err != nil {
		fatal(err)
	}
}
