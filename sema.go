package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// sema.go — go++ v2 compiler: semantic analysis.
//
// Architecture (skeleton §0-§9):
//   - two-phase collection: enums + function signatures first, bodies after
//     (forward references work)
//   - bidirectional type checking: checkExpr infers bottom-up, checkAgainst
//     propagates an expected type top-down (§6)
//   - tError poison: a failed check records one diagnostic and yields tError,
//     which unifies silently with everything — one bug, one message (§4)
//   - tNever bottom: divergence (panic) unifies with any type, so
//     `Err(e) -> panic(e)` is a valid value-producing match arm (§4)
//   - diagnostics are collected, never thrown: check() always returns
//     partial results (§0/§11)

// ---------- types ----------

type Type interface{ String() string }

type tBasic struct{ name string } // int, string, bool, duration, ...
func (t tBasic) String() string   { return t.name }

type tEnum struct { // instantiated enum: Status, Result[int, string]
	decl *EnumDecl
	args []Type
}

func (t *tEnum) String() string {
	if len(t.args) == 0 {
		return t.decl.Name
	}
	parts := make([]string, len(t.args))
	for i, a := range t.args {
		parts[i] = a.String()
	}
	return t.decl.Name + "[" + strings.Join(parts, ", ") + "]"
}

type tStruct struct{ decl *StructDecl } // nominal: identity is the decl

func (t *tStruct) String() string { return t.decl.Name }

type tMap struct{ k, v Type }

func (t *tMap) String() string { return "map[" + t.k.String() + "]" + t.v.String() }

type tChan struct{ elem Type }

func (t *tChan) String() string { return "chan " + t.elem.String() }

type tSlice struct{ elem Type }

func (t *tSlice) String() string { return "[]" + t.elem.String() }

type tStar struct{ x Type }

func (t *tStar) String() string { return "*" + t.x.String() }

type tFunc struct {
	params  []Type
	results []Type
}

func (t *tFunc) String() string {
	parts := make([]string, len(t.params))
	for i, p := range t.params {
		parts[i] = p.String()
	}
	r := ""
	if len(t.results) > 0 {
		r = " " + t.results[0].String()
	}
	return "func(" + strings.Join(parts, ", ") + ")" + r
}

type tTypeParam struct{ name string } // T inside a generic enum declaration
func (t tTypeParam) String() string   { return t.name }

type tVoid struct{}

func (tVoid) String() string { return "void" }

// tError is the poison type (§4): produced wherever a check failed. It
// compares equal to everything so one mistake yields exactly one
// diagnostic, never a cascade.
type tError struct{}

func (tError) String() string { return "<error>" }

// tNever is the bottom type (§4): the type of diverging expressions
// (panic). It unifies with everything, which is what lets
// `if x { 1 } else { panic() }` and match arms that panic typecheck.
type tNever struct{}

func (tNever) String() string { return "!" }

// tUntypedInt / tUntypedFloat are the types of numeric literals before
// context pins them down (§7). CHECK mode adopts the expected numeric type
// (with an overflow check); unconstrained use defaults via defaultType
// (int / float64). They never appear in declarations.
type tUntypedInt struct{}

func (tUntypedInt) String() string { return "untyped int" }

type tUntypedFloat struct{}

func (tUntypedFloat) String() string { return "untyped float" }

// defaultType pins an unconstrained untyped literal to its default (§7).
func defaultType(t Type) Type {
	switch t.(type) {
	case tUntypedInt:
		return tint
	case tUntypedFloat:
		return tfloat64
	}
	return t
}

func isErr(t Type) bool   { _, ok := t.(tError); return ok }
func isNever(t Type) bool { _, ok := t.(tNever); return ok }

var (
	tint      = tBasic{"int"}
	tstring   = tBasic{"string"}
	tbool     = tBasic{"bool"}
	tfloat64  = tBasic{"float64"}
	trune     = tBasic{"rune"}
	tduration = tBasic{"duration"}
	tvoid     = tVoid{}
	terr      = tError{}
)

// basicTypes are the types nameable in go++ source. error and any are
// deliberately absent (spec §5): failures are Result[T, E], absence is
// Option[T]. Emitted Go code still uses any for generic instantiations.
var basicTypes = map[string]bool{
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true, "byte": true, "rune": true,
	"string": true, "bool": true, "float32": true, "float64": true,
}

func sameType(a, b Type) bool {
	// poison unifies silently with everything (§4)
	if isErr(a) || isErr(b) {
		return true
	}
	switch at := a.(type) {
	case tBasic:
		bt, ok := b.(tBasic)
		return ok && at.name == bt.name
	case *tEnum:
		bt, ok := b.(*tEnum)
		if !ok || at.decl != bt.decl || len(at.args) != len(bt.args) {
			return false
		}
		for i := range at.args {
			if !sameType(at.args[i], bt.args[i]) {
				return false
			}
		}
		return true
	case *tStruct:
		bt, ok := b.(*tStruct)
		return ok && at.decl == bt.decl
	case *tMap:
		bt, ok := b.(*tMap)
		return ok && sameType(at.k, bt.k) && sameType(at.v, bt.v)
	case *tChan:
		bt, ok := b.(*tChan)
		return ok && sameType(at.elem, bt.elem)
	case *tSlice:
		bt, ok := b.(*tSlice)
		return ok && sameType(at.elem, bt.elem)
	case *tStar:
		bt, ok := b.(*tStar)
		return ok && sameType(at.x, bt.x)
	case tTypeParam:
		bt, ok := b.(tTypeParam)
		return ok && at.name == bt.name
	case tVoid:
		_, ok := b.(tVoid)
		return ok
	case tNever:
		_, ok := b.(tNever)
		return ok
	case tUntypedInt:
		_, ok := b.(tUntypedInt)
		return ok
	case tUntypedFloat:
		_, ok := b.(tUntypedFloat)
		return ok
	}
	return false
}

// subst replaces type parameters with concrete types.
func subst(ty Type, params []string, args []Type) Type {
	switch t := ty.(type) {
	case tTypeParam:
		for i, p := range params {
			if p == t.name {
				return args[i]
			}
		}
		return t
	case *tEnum:
		na := make([]Type, len(t.args))
		for i, a := range t.args {
			na[i] = subst(a, params, args)
		}
		return &tEnum{decl: t.decl, args: na}
	case *tMap:
		return &tMap{subst(t.k, params, args), subst(t.v, params, args)}
	case *tChan:
		return &tChan{subst(t.elem, params, args)}
	case *tSlice:
		return &tSlice{subst(t.elem, params, args)}
	case *tStar:
		return &tStar{subst(t.x, params, args)}
	}
	return ty
}

// ---------- scopes ----------

type scope struct {
	parent *scope
	vars   map[string]Type
	lines  map[string]int // declaration lines, for "redeclared" notes (§11)
}

func (s *scope) lookup(name string) (Type, bool) {
	for cur := s; cur != nil; cur = cur.parent {
		if t, ok := cur.vars[name]; ok {
			return t, true
		}
	}
	return nil, false
}

// ---------- checker ----------

type ctorTarget struct {
	enum    *EnumDecl
	variant *Variant
}

type checker struct {
	diag       *Diagnostics
	enums      map[string]*EnumDecl
	structs    map[string]*StructDecl
	prelude    map[*EnumDecl]bool // synthetic prelude enums (Result, Option)
	funcs      map[string]*tFunc
	ctors      map[string]*ctorTarget
	ambiguous  map[string]bool
	globals    *scope
	cur        *scope
	curResults  []Type
	curFuncLine int // declaration line of the function being checked (§11 notes)
	loopDepth   int
	// outputs for the emitter (side tables, §1)
	types      map[Expr]Type
	resolved   map[Expr]*ctorTarget // idents/call-funs that are variant references
	inferred   map[Expr][]Type      // ctor exprs whose type args were inferred (§8-lite)
	constVals  map[Expr]constVal    // comptime exprs -> compile-time values (§10)
	patVariant map[Pattern]bool     // IdentPat that matches a unit variant (not a binding)
	cycleDone  map[*StructDecl]bool // structs already reported on an infinite-size cycle
	preludeVars map[Expr]bool       // idents bound in the prelude (ms, second, minute)
	// §3 imports: qualifier -> dependency checker (checked before the
	// importer, so its funcs/ctors/enums tables are complete)
	imports     map[string]*checker
	importPaths map[string]string // qualifier -> output-relative Go import path
	qualified   map[Expr]string   // value exprs referencing a dependency (foo.Bar) -> qualifier
	declPkg     map[Decl]string   // foreign enum/struct decl -> owning package qualifier
	src         string            // package source (for comptime .body text)
}

func preludeEnums() []*EnumDecl {
	result := &EnumDecl{Name: "Result", TypeParams: []string{"T", "E"}, Variants: []Variant{
		{Name: "Ok", Fields: []Field{{Name: "v0", Type: &IdentType{Name: "T"}}}},
		{Name: "Err", Fields: []Field{{Name: "v0", Type: &IdentType{Name: "E"}}}},
	}}
	option := &EnumDecl{Name: "Option", TypeParams: []string{"T"}, Variants: []Variant{
		{Name: "Some", Fields: []Field{{Name: "v0", Type: &IdentType{Name: "T"}}}},
		{Name: "None"},
	}}
	return []*EnumDecl{result, option}
}

// sharedPrelude is the ONE pair of synthetic Result/Option declarations
// every checker shares (§3): Result values crossing package boundaries
// must keep type identity at the sema level too, not just in emitted Go.
var sharedPrelude = preludeEnums()

// exported reports whether a name is visible outside its package (§3:
// capitalized = exported, Go's rule).
func exported(name string) bool {
	return name != "" && name[0] >= 'A' && name[0] <= 'Z'
}

// check runs semantic analysis and ALWAYS returns partial results plus the
// collected diagnostics (§0): the caller decides whether to proceed to
// codegen (only when diags.HasErrors() is false).
func check(f *File) (*checker, *Diagnostics) {
	return checkImports(f, nil, nil, "")
}

// checkImports is check with a package context: imports maps qualifiers to
// already-checked dependency checkers, importPaths to their Go import
// paths for emission; src is the package source (comptime .body text).
func checkImports(f *File, imports map[string]*checker, importPaths map[string]string, src ...string) (*checker, *Diagnostics) {
	c := &checker{
		diag:       &Diagnostics{},
		enums:      map[string]*EnumDecl{},
		structs:    map[string]*StructDecl{},
		prelude:    map[*EnumDecl]bool{},
		funcs:      map[string]*tFunc{},
		ctors:      map[string]*ctorTarget{},
		ambiguous:  map[string]bool{},
		globals:    &scope{vars: map[string]Type{}},
		types:      map[Expr]Type{},
		resolved:   map[Expr]*ctorTarget{},
		inferred:   map[Expr][]Type{},
		constVals:  map[Expr]constVal{},
		patVariant: map[Pattern]bool{},
		preludeVars: map[Expr]bool{},
		imports:     map[string]*checker{},
		importPaths: map[string]string{},
		qualified:   map[Expr]string{},
		declPkg:     map[Decl]string{},
	}
	for qual, dep := range imports {
		c.imports[qual] = dep
	}
	for qual, path := range importPaths {
		c.importPaths[qual] = path
	}
	if len(src) > 0 {
		c.src = src[0]
	}
	// §10 metaprogramming: comptime blocks run BEFORE any registration or
	// resolution, so their mutations and gen'd declarations are exactly
	// what the rest of the pipeline sees
	c.evalComptimeDecls(f)
	// register foreign decls so the emitter qualifies their references
	for qual, dep := range c.imports {
		for _, e := range dep.enums {
			if !dep.prelude[e] {
				c.declPkg[e] = qual
			}
		}
		for _, s := range dep.structs {
			c.declPkg[s] = qual
		}
	}
	for _, e := range sharedPrelude {
		c.enums[e.Name] = e
		c.prelude[e] = true
	}
	c.globals.vars["ms"] = tduration
	c.globals.vars["second"] = tduration
	c.globals.vars["minute"] = tduration
	// pass 1: register user enums and structs — one Type namespace
	// (duplicate -> error, keep first, continue)
	for _, d := range f.Decls {
		switch dt := d.(type) {
		case *EnumDecl:
			if _, dup := c.enums[dt.Name]; dup {
				c.diag.errorf(dt.Line, "duplicate type %s", dt.Name)
				continue
			}
			if _, dup := c.structs[dt.Name]; dup {
				c.diag.errorf(dt.Line, "duplicate type %s", dt.Name)
				continue
			}
			c.enums[dt.Name] = dt
		case *StructDecl:
			if _, dup := c.structs[dt.Name]; dup {
				c.diag.errorf(dt.Line, "duplicate type %s", dt.Name)
				continue
			}
			if _, dup := c.enums[dt.Name]; dup {
				c.diag.errorf(dt.Line, "duplicate type %s", dt.Name)
				continue
			}
			c.structs[dt.Name] = dt
		}
	}
	// variant constructor index
	for _, e := range c.enums {
		for i := range e.Variants {
			v := &e.Variants[i]
			if _, exists := c.ctors[v.Name]; exists {
				c.ambiguous[v.Name] = true
			} else {
				c.ctors[v.Name] = &ctorTarget{enum: e, variant: v}
			}
		}
	}
	// enum field types must resolve (with type params in scope)
	for _, e := range c.enums {
		if c.prelude[e] {
			continue
		}
		for _, v := range e.Variants {
			for _, fld := range v.Fields {
				c.resolveTypeIn(fld.Type, e)
			}
		}
	}
	// struct field types must resolve
	for _, s := range c.structs {
		for _, fld := range s.Fields {
			c.resolveType(fld.Type)
		}
	}
	// §4: infinite-size type cycle detection — a struct that contains
	// itself without indirection (Ptr, slice, map, chan) cannot exist.
	// One diagnostic per cycle: every struct on a detected cycle's path
	// is marked so it isn't re-reported as another root.
	c.cycleDone = map[*StructDecl]bool{}
	names := make([]string, 0, len(c.structs))
	for n := range c.structs {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic diagnostics, not map order
	for _, n := range names {
		s := c.structs[n]
		if !c.cycleDone[s] {
			c.checkStructCycles(s, s, map[*StructDecl]bool{})
		}
	}
	// pass 1.5: function signatures (no bodies — mutual recursion works)
	for _, d := range f.Decls {
		if fn, ok := d.(*FuncDecl); ok {
			if _, dup := c.funcs[fn.Name]; dup {
				c.diag.errorf(fn.Line, "duplicate function %s", fn.Name)
				continue
			}
			ft := &tFunc{}
			for _, p := range fn.Params {
				ft.params = append(ft.params, c.resolveType(p.Type))
			}
			for _, r := range fn.Results {
				ft.results = append(ft.results, c.resolveType(r.Type))
			}
			c.funcs[fn.Name] = ft
		}
	}
	// pass 2: function bodies
	for _, d := range f.Decls {
		if fn, ok := d.(*FuncDecl); ok {
			ft := c.funcs[fn.Name]
			if ft == nil { // duplicate definition; first one was checked
				continue
			}
			c.curResults = ft.results
			c.curFuncLine = fn.Line // for "expected because of this" notes (§11)
			c.cur = &scope{parent: c.globals, vars: map[string]Type{}}
			for i, p := range fn.Params {
				if i < len(ft.params) {
					c.cur.vars[p.Name] = ft.params[i]
				}
			}
			c.checkStmts(fn.Body.List)
		}
	}
	// pass 3 (§9): flow checks over the typed bodies
	c.checkFlow(f)
	return c, c.diag
}

// checkStructCycles: DFS over direct (unboxed) struct fields; a cycle
// means infinite size. Indirection through *T/map/slice/chan breaks it.
func (c *checker) checkStructCycles(root, cur *StructDecl, visiting map[*StructDecl]bool) {
	if visiting[cur] {
		c.diag.errorf(cur.Line, "recursive type has infinite size: %s contains itself (insert indirection, e.g. *%s)", root.Name, cur.Name)
		for s := range visiting {
			c.cycleDone[s] = true
		}
		return
	}
	visiting[cur] = true
	defer delete(visiting, cur)
	for _, f := range cur.Fields {
		if st, ok := c.resolveType(f.Type).(*tStruct); ok && c.declPkg[st.decl] == "" {
			// cycles can't cross packages (imports are acyclic, §3),
			// so foreign structs are never re-entered here
			c.checkStructCycles(root, st.decl, visiting)
		}
	}
}

func structField(s *StructDecl, name string) *Field {
	for i := range s.Fields {
		if s.Fields[i].Name == name {
			return &s.Fields[i]
		}
	}
	return nil
}

func (c *checker) resolveType(te TypeExpr) Type { return c.resolveTypeIn(te, nil) }

func (c *checker) resolveTypeIn(te TypeExpr, inEnum *EnumDecl) Type {
	switch t := te.(type) {
	case *IdentType:
		if inEnum != nil {
			for _, tp := range inEnum.TypeParams {
				if tp == t.Name {
					return tTypeParam{t.Name}
				}
			}
		}
		if dot := strings.IndexByte(t.Name, '.'); dot > 0 { // pkg.Type (§3)
			return c.resolveQualifiedType(t.Name[:dot], t.Name[dot+1:], nil, t.Line, t.Col)
		}
		if basicTypes[t.Name] {
			return tBasic{t.Name}
		}
		if e, ok := c.enums[t.Name]; ok {
			if len(e.TypeParams) > 0 {
				c.diag.errorfAt(t.Line, t.Col, "enum %s is generic: use %s[%s]", t.Name, t.Name, strings.Join(e.TypeParams, ", "))
				return terr
			}
			return &tEnum{decl: e}
		}
		if s, ok := c.structs[t.Name]; ok {
			return &tStruct{decl: s}
		}
		c.diag.errorfAt(t.Line, t.Col, "undefined type %s", t.Name)
		return terr
	case *IndexType:
		base, ok := t.X.(*IdentType)
		if !ok {
			c.diag.errorfAt(t.Line, t.Col, "invalid generic type")
			return terr
		}
		var args []Type
		for _, a := range t.Args {
			args = append(args, c.resolveTypeIn(a, inEnum))
		}
		if dot := strings.IndexByte(base.Name, '.'); dot > 0 { // pkg.Box[T] (§3)
			return c.resolveQualifiedType(base.Name[:dot], base.Name[dot+1:], args, t.Line, t.Col)
		}
		e, ok := c.enums[base.Name]
		if !ok {
			c.diag.errorfAt(t.Line, t.Col, "%s is not a generic enum", base.Name)
			return terr
		}
		if len(e.TypeParams) != len(t.Args) { // arity check (§5)
			c.diag.errorfAt(t.Line, t.Col, "%s takes %d type argument(s), got %d", base.Name, len(e.TypeParams), len(t.Args))
			return terr
		}
		return &tEnum{decl: e, args: args}
	case *MapType:
		return &tMap{c.resolveTypeIn(t.K, inEnum), c.resolveTypeIn(t.V, inEnum)}
	case *ChanType:
		return &tChan{c.resolveTypeIn(t.Elem, inEnum)}
	case *SliceType:
		return &tSlice{c.resolveTypeIn(t.Elem, inEnum)}
	case *StarType:
		return &tStar{c.resolveTypeIn(t.X, inEnum)}
	}
	c.diag.errorf(0, "unknown type expression")
	return terr
}

// resolveQualifiedType resolves pkg.Name (and pkg.Name[args]) against an
// imported package's type namespace (§3). Only exported, non-prelude
// types are visible.
func (c *checker) resolveQualifiedType(pkg, name string, args []Type, line, col int) Type {
	dep, ok := c.imports[pkg]
	if !ok {
		c.diag.errorfAt(line, col, "undefined package %s", pkg)
		return terr
	}
	if !exported(name) {
		c.diag.errorfAt(line, col, "%s.%s is not exported", pkg, name)
		return terr
	}
	if e, ok := dep.enums[name]; ok && !dep.prelude[e] {
		if len(e.TypeParams) != len(args) { // arity (§5), covers bare pkg.Generic too
			c.diag.errorfAt(line, col, "%s.%s takes %d type argument(s), got %d", pkg, name, len(e.TypeParams), len(args))
			return terr
		}
		return &tEnum{decl: e, args: args}
	}
	if s, ok := dep.structs[name]; ok {
		if len(args) > 0 {
			c.diag.errorfAt(line, col, "%s.%s is not generic", pkg, name)
			return terr
		}
		return &tStruct{decl: s}
	}
	c.diag.errorfAt(line, col, "undefined type %s.%s", pkg, name)
	return terr
}

// packageOf reports whether id names an imported package — only when no
// local binding shadows the qualifier (§3: locals win, like Go).
func (c *checker) packageOf(id *Ident) (*checker, bool) {
	if _, shadowed := c.cur.lookup(id.Name); shadowed {
		return nil, false
	}
	dep, ok := c.imports[id.Name]
	return dep, ok
}

// exprToType converts a parsed expression back into a type expression,
// for generic instantiations like Ok[int, string].
func exprToType(e Expr) TypeExpr {
	switch ex := e.(type) {
	case *Ident:
		return &IdentType{Name: ex.Name, Line: ex.Line, Col: ex.Col}
	case *SelectorExpr:
		// pkg.Type as a type argument: parser flattens these in parseType,
		// but they also arrive via expression parsing (foo.Box[int](...))
		if id, ok := ex.X.(*Ident); ok {
			return &IdentType{Name: id.Name + "." + ex.Sel, Line: ex.Line, Col: ex.Col}
		}
		return nil
	case *IndexExpr:
		base := exprToType(ex.X)
		if base == nil {
			return nil
		}
		it := &IndexType{X: base, Line: ex.Line, Col: ex.Col}
		for _, a := range ex.Index {
			at := exprToType(a)
			if at == nil {
				return nil
			}
			it.Args = append(it.Args, at)
		}
		return it
	}
	return nil
}

// ---------- statements ----------

func (c *checker) checkStmts(list []Stmt) {
	for _, s := range list {
		c.checkStmt(s)
	}
}

func (c *checker) child() *scope {
	c.cur = &scope{parent: c.cur, vars: map[string]Type{}}
	return c.cur
}

func (c *checker) pop() { c.cur = c.cur.parent }

// declareShort binds a name for :=, enforcing the shadowing policy
// (§3/§29: allowed across scopes, an error within the same scope) and
// pointing at the previous declaration when it fires (§11).
func (c *checker) declareShort(id *Ident, t Type) {
	if id.Name == "_" {
		return // the blank identifier is assignable to, never read
	}
	if _, dup := c.cur.vars[id.Name]; dup {
		d := c.diag.errorfAt(id.Line, id.Col, "%s redeclared in this scope", id.Name)
		if prev, ok := c.cur.lines[id.Name]; ok {
			d.note(prev, 0, "previous declaration of "+id.Name+" here")
		}
	}
	c.cur.vars[id.Name] = t
	if c.cur.lines == nil {
		c.cur.lines = map[string]int{}
	}
	c.cur.lines[id.Name] = id.Line
}

func (c *checker) checkStmt(s Stmt) {
	switch st := s.(type) {
	case *Block:
		c.child()
		c.checkStmts(st.List)
		c.pop()
	case *ForInStmt:
		c.diag.errorfAt(st.Line, st.Col, "for-in is only supported inside comptime blocks")
	case *VarStmt:
		ty := c.resolveType(st.Type)
		if st.Init != nil {
			if te, ok := st.Init.(*TryExpr); ok {
				rt := c.checkTry(te)
				c.expect(rt, ty, te.Line, te.Col)
			} else {
				c.checkAgainst(st.Init, ty)
			}
		}
		c.cur.vars[st.Name] = ty
	case *AssignStmt:
		if len(st.Lhs) != len(st.Rhs) {
			c.diag.errorfAt(st.Line, st.Col, "assignment mismatch: %d left, %d right", len(st.Lhs), len(st.Rhs))
			return
		}
		for i := range st.Lhs {
			if te, ok := st.Rhs[i].(*TryExpr); ok {
				// `x := f()?` — only as the direct rhs of a single
				// assignment; the desugar needs statement position
				if len(st.Lhs) != 1 || (st.Op != ":=" && st.Op != "=") {
					c.diag.errorfAt(te.Line, te.Col, "? can only be used in a simple := or = assignment")
					c.checkExpr(te.X)
					continue
				}
				rt := c.checkTry(te)
				if st.Op == ":=" {
					id, ok := st.Lhs[i].(*Ident)
					if !ok {
						c.diag.errorfAt(st.Line, st.Col, "left side of := must be a name")
						continue
					}
					c.declareShort(id, rt)
				} else {
					if id, ok := st.Lhs[i].(*Ident); ok && id.Name == "_" {
						continue
					}
					lt := c.checkExpr(st.Lhs[i])
					c.expect(rt, lt, te.Line, te.Col)
				}
				continue
			}
			if st.Op == ":=" {
				rt := defaultType(c.checkExpr(st.Rhs[i]))
				if mx, ok := st.Rhs[i].(*MatchExpr); ok && sameType(rt, tvoid) {
					c.diag.errorfAt(mx.Line, mx.Col, "match in value context must produce a value in every arm")
				}
				id, ok := st.Lhs[i].(*Ident)
				if !ok {
					c.diag.errorfAt(st.Line, st.Col, "left side of := must be a name")
					continue
				}
				c.declareShort(id, rt)
			} else {
				// the blank identifier is assignable to, never read
				if id, ok := st.Lhs[i].(*Ident); ok && id.Name == "_" {
					c.checkExpr(st.Rhs[i])
					continue
				}
				lt := c.checkExpr(st.Lhs[i])
				if st.Op == "=" {
					c.checkAgainst(st.Rhs[i], lt)
				} else { // +=, -=, ...
					rt := c.checkExpr(st.Rhs[i])
					c.expect(rt, lt, lineOf(st.Rhs[i]), colOf(st.Rhs[i]))
				}
			}
		}
	case *ExprStmt:
		if te, ok := st.X.(*TryExpr); ok {
			c.checkTry(te) // value discarded; Err still propagates
		} else {
			c.checkExpr(st.X)
		}
	case *IfStmt:
		ct := c.checkExpr(st.Cond)
		c.expectBool(ct, lineOf(st.Cond), colOf(st.Cond), "if condition")
		c.child()
		c.checkStmts(st.Then.List)
		c.pop()
		if st.Else != nil {
			c.checkStmt(st.Else)
		}
	case *ForStmt:
		c.child()
		if st.Init != nil {
			c.checkStmt(st.Init)
		}
		if st.Cond != nil {
			ct := c.checkExpr(st.Cond)
			c.expectBool(ct, lineOf(st.Cond), colOf(st.Cond), "for condition")
		}
		if st.Post != nil {
			c.checkStmt(st.Post)
		}
		c.checkStmts(st.Body.List)
		c.pop()
	case *LoopStmt:
		c.child()
		c.loopDepth++
		c.checkStmts(st.Body.List)
		c.loopDepth--
		c.pop()
	case *BreakStmt:
		if st.Label == "loop" {
			if c.loopDepth == 0 {
				c.diag.errorfAt(st.Line, st.Col, "break loop outside of a loop block")
			}
		} else if st.Label != "" {
			c.diag.errorfAt(st.Line, st.Col, "unknown label %s", st.Label)
		}
	case *ReturnStmt:
		if len(c.curResults) == 0 {
			if len(st.Results) != 0 {
				c.diag.errorfAt(st.Line, st.Col, "function has no results, return has %d", len(st.Results))
			}
			return
		}
		if len(st.Results) != len(c.curResults) {
			c.diag.errorfAt(st.Line, st.Col, "return has %d value(s), function declares %d", len(st.Results), len(c.curResults))
			return
		}
		for i, r := range st.Results {
			// a mismatch here is explained by the signature (§11):
			// attach "expected because of this" to any new diagnostic
			before := len(c.diag.items)
			c.checkAgainst(r, c.curResults[i])
			for k := before; k < len(c.diag.items); k++ {
				c.diag.items[k].note(c.curFuncLine, 0, "because of the return type declared here")
			}
		}
	case *IncDecStmt:
		xt := c.checkExpr(st.X)
		if !isErr(xt) && !isNumeric(xt) {
			c.diag.errorfAt(st.Line, st.Col, "%s needs a number, got %s", st.Op, xt)
		}
	}
}

func isNumeric(t Type) bool {
	switch t.(type) {
	case tUntypedInt, tUntypedFloat:
		return true // literals are numeric before they are pinned (§7)
	}
	if b, ok := t.(tBasic); ok {
		switch b.name {
		case "int", "int8", "int16", "int32", "int64",
			"uint", "uint8", "uint16", "uint32", "uint64",
			"float32", "float64", "byte", "rune", "duration":
			return true
		}
	}
	return false
}

// isInteger reports whether t is an integer type (typed or an untyped
// int constant): the operand requirement for %, bit ops, and shifts (§7).
func isInteger(t Type) bool {
	if _, ok := t.(tUntypedInt); ok {
		return true
	}
	if b, ok := t.(tBasic); ok {
		return isSizedInt(b.name)
	}
	return false
}

func isFloat(t Type) bool {
	if b, ok := t.(tBasic); ok {
		return b.name == "float32" || b.name == "float64"
	}
	return false
}

// intConstFits reports whether an integer literal (Go syntax: 0x/0o/0b
// prefixes and _ separators), negated when neg, fits the named numeric
// type. Literals beyond uint64 magnitude fail ParseUint and are left for
// the Go backend to reject — still exactly one diagnostic.
func intConstFits(text string, neg bool, t Type) bool {
	mag, err := strconv.ParseUint(text, 0, 64)
	if err != nil {
		return true
	}
	b, ok := t.(tBasic)
	if !ok {
		return true
	}
	var posLim, negLim uint64 // largest magnitude allowed plain / negated
	switch b.name {
	case "int8":
		posLim, negLim = 1<<7-1, 1<<7
	case "int16":
		posLim, negLim = 1<<15-1, 1<<15
	case "int32", "rune":
		posLim, negLim = 1<<31-1, 1<<31
	case "int64", "int", "duration":
		posLim, negLim = 1<<63-1, 1<<63
	case "uint8", "byte":
		posLim = 1<<8 - 1
	case "uint16":
		posLim = 1<<16 - 1
	case "uint32":
		posLim = 1<<32 - 1
	case "uint64", "uint", "uintptr":
		posLim = 1<<64 - 1
	default:
		return true // floats: any integer literal is close enough
	}
	if neg {
		return mag <= negLim // 0 for unsigned: -0 fits, -1 does not
	}
	return mag <= posLim
}

// expect verifies `from` is assignable to `to`; silent when either side is
// poisoned (§4) or `from` diverges (tNever unifies with everything).
func (c *checker) expect(from, to Type, line, col int) {
	if sameType(from, to) || isNever(from) {
		return
	}
	// untyped constants adopt any numeric type they flow into (§7);
	// anything else is strict — no implicit conversions between typed values
	switch from.(type) {
	case tUntypedInt:
		if isNumeric(to) {
			return
		}
	case tUntypedFloat:
		if isFloat(to) {
			return
		}
	}
	c.diag.errorfAt(line, col, "expected %s, found %s", to, from)
}

func (c *checker) expectBool(t Type, line, col int, what string) {
	if isErr(t) {
		return
	}
	if !sameType(t, tbool) {
		c.diag.errorfAt(line, col, "%s must be bool, got %s", what, t)
	}
}

// ---------- expressions ----------

// checkAgainst is CHECK mode (§6): verify e against an expected type,
// pushing context downward. Integer/float literals adopt the expected
// numeric type (literal defaulting, §7); match expressions check every
// arm against it. Signatures and declarations are the blame boundaries.
// The blame position is e's own, so the caret lands on the offending
// expression rather than the enclosing statement (§11).
func (c *checker) checkAgainst(e Expr, expected Type) Type {
	line, col := lineOf(e), colOf(e)
	if t, ok := c.tryAdopt(e, expected); ok {
		return t
	}
	switch ex := e.(type) {
	case *MatchExpr:
		t := c.checkMatchWant(ex, expected)
		c.types[e] = t // checkExpr records this; the CHECK path must too
		return t
	case *Ident:
		// a bare generic unit variant (None) solves from the expected
		// type: var o Option[int] = None. In infer mode it stays an error.
		if ct, ok := c.ctors[ex.Name]; ok && len(ct.enum.TypeParams) > 0 && len(ct.variant.Fields) == 0 {
			if en, ok2 := expected.(*tEnum); ok2 && en.decl == ct.enum && len(en.args) == len(ct.enum.TypeParams) {
				if c.ambiguous[ex.Name] {
					c.diag.errorfAt(line, col, "variant name %s is ambiguous (multiple enums)", ex.Name)
					c.types[e] = terr
					return terr
				}
				c.resolved[ex] = ct
				c.inferred[ex] = en.args
				c.types[e] = expected
				return expected
			}
		}
	case *CallExpr:
		// the expected type flows into the call so generic constructors
		// can infer their type arguments: var r Result[int, string] = Ok(1)
		t := c.checkCall(ex, expected)
		c.types[e] = t
		c.expect(t, expected, line, col)
		return t
	case *ComptimeExpr:
		// evaluate, then range-check the constant against the declared
		// type: var x int8 = comptime 100 + 100 is an error
		t := c.checkComptime(ex)
		c.types[e] = t
		if !isErr(t) {
			if v, ok := c.constVals[ex]; ok && v.kind == ckInt {
				if b, ok2 := expected.(tBasic); ok2 && isSizedInt(b.name) && !fitsBigInt(v.i, b.name) {
					c.diag.errorfAt(line, col, "constant %s overflows %s", v.i.String(), expected)
					c.types[e] = terr
					return terr
				}
			}
			c.expect(t, expected, line, col)
		}
		return t
	}
	t := c.checkExpr(e)
	c.expect(t, expected, line, col)
	return t
}

// tryAdopt handles expression shapes that need the expected type itself:
// integer/float literals (and signed literals) adopt it, with a
// compile-time overflow check (§7). ok=false means e is not one of those
// shapes — the caller falls back to infer + expect.
func (c *checker) tryAdopt(e Expr, expected Type) (Type, bool) {
	if lit, ok := e.(*BasicLit); ok {
		switch lit.Kind {
		case kInt:
			if isNumeric(expected) {
				if !intConstFits(lit.Value, false, expected) {
					c.diag.errorfAt(lit.Line, lit.Col, "constant %s overflows %s", lit.Value, expected)
					c.types[e] = terr
					return terr, true
				}
				c.types[e] = expected
				return expected, true
			}
		case kFloat:
			if isFloat(expected) {
				c.types[e] = expected
				return expected, true
			}
		}
		return nil, false
	}
	if un, ok := e.(*UnaryExpr); ok && (un.Op == "-" || un.Op == "+") {
		if lit, ok := un.X.(*BasicLit); ok {
			neg := un.Op == "-"
			switch lit.Kind {
			case kInt:
				if isNumeric(expected) {
					if !intConstFits(lit.Value, neg, expected) {
						sign := "+"
						if neg {
							sign = "-"
						}
						c.diag.errorfAt(un.Line, un.Col, "constant %s%s overflows %s", sign, lit.Value, expected)
						c.types[e] = terr
						return terr, true
					}
					c.types[e] = expected
					return expected, true
				}
			case kFloat:
				if isFloat(expected) {
					c.types[e] = expected
					return expected, true
				}
			}
		}
	}
	return nil, false
}

// checkTry checks `expr?` in statement position (spec §7): the operand
// must be Result[T, E], the enclosing function must return Result[_, E]
// with a matching E, and the expression has type T. On Err the emitted
// code returns Err(e) from the function early.
func (c *checker) checkTry(te *TryExpr) Type {
	xt := c.checkExpr(te.X)
	var ty Type = terr
	if isErr(xt) {
		ty = terr
	} else if en, ok := xt.(*tEnum); !ok || en.decl.Name != "Result" || len(en.args) != 2 {
		c.diag.errorfAt(te.Line, te.Col, "? needs a Result[T, E], got %s", xt)
	} else {
		e := en.args[1]
		bad := false
		if len(c.curResults) != 1 {
			bad = true
		} else if rt, ok := c.curResults[0].(*tEnum); !ok || rt.decl.Name != "Result" || len(rt.args) != 2 {
			bad = true
		} else {
			c.expect(e, rt.args[1], te.Line, te.Col) // error types must match
		}
		if bad {
			c.diag.errorfAt(te.Line, te.Col, "? requires the function to return Result[T, %s]", e)
		} else {
			ty = en.args[0]
		}
	}
	c.types[te] = ty
	return ty
}

func (c *checker) checkExprIn(e Expr, s *scope) Type {
	save := c.cur
	c.cur = s
	t := c.checkExpr(e)
	c.cur = save
	return t
}

func (c *checker) checkExpr(e Expr) Type {
	var ty Type
	switch ex := e.(type) {
	case *BasicLit:
		switch ex.Kind {
		case kInt:
			ty = tUntypedInt{} // context pins it; unconstrained defaults to int (§7)
		case kFloat:
			ty = tUntypedFloat{}
		case kString:
			ty = tstring
		case kRune:
			ty = trune
		}
	case *Ident:
		ty = c.checkIdentValue(ex)
	case *BinaryExpr:
		xt := c.checkExpr(ex.X)
		yt := c.checkExpr(ex.Y)
		if isErr(xt) || isErr(yt) {
			ty = terr
			break
		}
		// no implicit conversions (§7): operands must agree
		switch ex.Op {
		case "&&", "||":
			c.expectBool(xt, lineOf(ex.X), colOf(ex.X), "left side of "+ex.Op)
			c.expectBool(yt, lineOf(ex.Y), colOf(ex.Y), "right side of "+ex.Op)
			ty = tbool
		case "==", "!=":
			c.expect(yt, xt, ex.Line, ex.Col)
			ty = tbool
		case "<", "<=", ">", ">=":
			if sameType(xt, tstring) && sameType(yt, tstring) {
				ty = tbool
			} else if arithType(xt, yt) != nil {
				ty = tbool
			} else {
				c.diag.errorfAt(ex.Line, ex.Col, "invalid comparison: %s %s %s (mismatched types)", xt, ex.Op, yt)
				ty = terr
			}
		case "+":
			if sameType(xt, tstring) && sameType(yt, tstring) {
				ty = tstring
			} else if at := arithType(xt, yt); at != nil {
				ty = at
			} else {
				c.diag.errorfAt(ex.Line, ex.Col, "invalid operation: %s + %s (mismatched types)", xt, yt)
				ty = terr
			}
		case "%", "&", "|", "^", "&^", "<<", ">>":
			// integer operands only — no float % or float shifts (§7)
			if !isInteger(xt) || !isInteger(yt) {
				c.diag.errorfAt(ex.Line, ex.Col, "invalid operation: %s %s %s (integer operands only)", xt, ex.Op, yt)
				ty = terr
			} else if at := arithType(xt, yt); at != nil {
				ty = at
			} else {
				c.diag.errorfAt(ex.Line, ex.Col, "invalid operation: %s %s %s (mismatched types)", xt, ex.Op, yt)
				ty = terr
			}
		default: // -, *, /
			if at := arithType(xt, yt); at != nil {
				ty = at
			} else {
				c.diag.errorfAt(ex.Line, ex.Col, "invalid operation: %s %s %s (mismatched types)", xt, ex.Op, yt)
				ty = terr
			}
		}
	case *UnaryExpr:
		xt := c.checkExpr(ex.X)
		switch {
		case isErr(xt):
			ty = terr
		default:
			switch ex.Op {
			case "!":
				ty = tbool
			case "&":
				ty = &tStar{x: xt}
			case "*":
				if st, ok := xt.(*tStar); ok {
					ty = st.x
				} else {
					c.diag.errorfAt(ex.Line, ex.Col, "cannot dereference non-pointer %s", xt)
					ty = terr
				}
			default:
				ty = xt
			}
		}
	case *CallExpr:
		ty = c.checkCall(ex, nil)
	case *SelectorExpr:
		ty = c.checkSelector(ex)
	case *IndexExpr:
		ty = c.checkIndex(ex)
	case *MakeChanExpr:
		et := c.resolveType(ex.Elem)
		if ex.Cap != nil {
			ct := c.checkExpr(ex.Cap)
			if !isErr(ct) && !isNumeric(ct) {
				c.diag.errorfAt(ex.Line, ex.Col, "channel capacity must be a number, got %s", ct)
			}
		}
		if isErr(et) {
			ty = terr
		} else {
			ty = &tChan{elem: et}
		}
	case *MatchExpr:
		// infer mode: an all-literal match materializes at its default
		// type — the emitter needs a concrete Go type for the iife
		ty = defaultType(c.checkMatchWant(ex, nil))
	case *StructLitExpr:
		ty = c.checkStructLit(ex)
	case *TryExpr:
		c.diag.errorfAt(ex.Line, ex.Col, "? can only be used directly on the right side of := / = / var, or as a statement")
		c.checkExpr(ex.X)
		ty = terr
	case *ComptimeExpr:
		ty = c.checkComptime(ex)
	default:
		c.diag.errorf(0, "unhandled expression %T", e)
		ty = terr
	}
	c.types[e] = ty
	return ty
}

// arithType merges numeric operand types (§7): untyped constants yield to
// the typed operand; identical types pass through; duration absorbs any
// numeric (it is an int64 count, and d*3 must stay convenient). Anything
// else returns nil and the caller reports "mismatched types" — mixing two
// different typed numerics (int8 + int64) is an error, not a conversion.
func arithType(x, y Type) Type {
	if (sameType(x, tduration) && isNumeric(y)) || (sameType(y, tduration) && isNumeric(x)) {
		return tduration
	}
	_, xUI := x.(tUntypedInt)
	_, yUI := y.(tUntypedInt)
	_, xUF := x.(tUntypedFloat)
	_, yUF := y.(tUntypedFloat)
	switch {
	case xUI && yUI:
		return tUntypedInt{}
	case (xUI || xUF) && (yUI || yUF): // both untyped, one a float
		return tUntypedFloat{}
	case xUI && isNumeric(y):
		return y
	case yUI && isNumeric(x):
		return x
	case xUF && isFloat(y):
		return y
	case yUF && isFloat(x):
		return x
	case sameType(x, y) && isNumeric(x):
		return x
	}
	return nil
}

func (c *checker) checkIdentValue(ex *Ident) Type {
	if t, ok := c.cur.lookup(ex.Name); ok {
		// prelude vars (ms/second/minute) live in the globals scope; the
		// emitter qualifies them as gopp.Ms etc. since user code lands in
		// its own package now
		for s := c.cur; s != nil; s = s.parent {
			if _, ok := s.vars[ex.Name]; ok {
				if s == c.globals {
					c.preludeVars[ex] = true
				}
				break
			}
		}
		return t
	}
	switch ex.Name {
	case "true", "false":
		return tbool
	}
	if ft, ok := c.funcs[ex.Name]; ok {
		return ft
	}
	if ct, ok := c.ctors[ex.Name]; ok {
		if c.ambiguous[ex.Name] {
			c.diag.errorfAt(ex.Line, ex.Col, "variant name %s is ambiguous (multiple enums)", ex.Name)
			return terr
		}
		c.resolved[ex] = ct
		et := &tEnum{decl: ct.enum}
		if len(ct.enum.TypeParams) > 0 {
			c.diag.errorfAt(ex.Line, ex.Col, "%s is generic; use explicit type arguments like %s[..](...)", ex.Name, ex.Name)
			return terr
		}
		if len(ct.variant.Fields) == 0 {
			return et // unit variant value
		}
		// constructor function value
		ft := &tFunc{results: []Type{et}}
		for _, f := range ct.variant.Fields {
			ft.params = append(ft.params, c.resolveTypeIn(f.Type, ct.enum))
		}
		return ft
	}
	if _, isPkg := c.imports[ex.Name]; isPkg {
		c.diag.errorfAt(ex.Line, ex.Col, "package %s is not a value", ex.Name)
		return terr
	}
	d := c.diag.errorfAt(ex.Line, ex.Col, "undefined: %s", ex.Name)
	if sug := c.suggestName(ex.Name); sug != "" {
		d.note(0, 0, "did you mean "+sug+"?")
	}
	return terr
}

// suggestName finds the closest visible name by edit distance (§11).
// Deterministic: candidates are considered in sorted order and ties
// keep the first.
func (c *checker) suggestName(name string) string {
	seen := map[string]bool{}
	var cands []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			cands = append(cands, s)
		}
	}
	for sc := c.cur; sc != nil; sc = sc.parent {
		for v := range sc.vars {
			add(v)
		}
	}
	for f := range c.funcs {
		add(f)
	}
	for ctor := range c.ctors {
		add(ctor)
	}
	for _, b := range []string{"println", "print", "panic", "len", "cap", "append", "true", "false"} {
		add(b)
	}
	sort.Strings(cands)
	best, bestDist := "", len(name)/3+1
	if bestDist < 2 {
		bestDist = 2
	}
	for _, cand := range cands {
		if d := editDistance(name, cand); d < bestDist {
			best, bestDist = cand, d
		}
	}
	return best
}

// editDistance is the classic Levenshtein DP over runes.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur := make([]int, len(br)+1)
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(min(cur[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(br)]
}

// checkCall checks a call. want is the expected type in CHECK mode (nil in
// infer mode) — generic constructors use it to seed type-arg inference.
func (c *checker) checkCall(ex *CallExpr, want Type) Type {
	switch fun := ex.Fun.(type) {
	case *Ident:
		switch fun.Name {
		case "println", "print":
			for _, a := range ex.Args {
				c.checkExpr(a)
			}
			return tvoid
		case "panic":
			for _, a := range ex.Args {
				c.checkExpr(a)
			}
			return tNever{} // diverges: unifies with any expected type (§4)
		case "len", "cap":
			if len(ex.Args) != 1 {
				c.diag.errorfAt(ex.Line, ex.Col, "%s takes 1 argument", fun.Name)
				return terr
			}
			c.checkExpr(ex.Args[0])
			return tint
		case "append":
			if len(ex.Args) < 1 {
				c.diag.errorfAt(ex.Line, ex.Col, "append needs arguments")
				return terr
			}
			return c.checkExpr(ex.Args[0])
		}
		if basicTypes[fun.Name] {
			// a type name in call position is an explicit conversion (§7) —
			// the only sanctioned way to mix numeric widths
			return c.checkConversion(ex, fun.Name)
		}
		if ft, ok := c.funcs[fun.Name]; ok {
			c.checkCallArgs(ex, ft.params)
			if len(ft.results) > 0 {
				return ft.results[0]
			}
			return tvoid
		}
		if ct, ok := c.ctors[fun.Name]; ok {
			return c.callVariantCtor(ex, fun, fun.Name, c, ct, nil, want)
		}
		c.diag.errorfAt(ex.Line, ex.Col, "undefined function: %s", fun.Name)
		for _, a := range ex.Args {
			c.checkExpr(a)
		}
		return terr
	case *IndexExpr:
		// generic constructor instantiation: Ok[int, string](v)
		if id, ok := fun.X.(*Ident); ok {
			if ct, ok := c.ctors[id.Name]; ok {
				args := c.resolveTypeArgs(ex, fun)
				if args == nil {
					return terr
				}
				return c.callVariantCtor(ex, id, id.Name, c, ct, args, want)
			}
		}
		// qualified generic constructor: foo.Box[int](v) (§3)
		if sel, ok := fun.X.(*SelectorExpr); ok {
			if id, ok2 := sel.X.(*Ident); ok2 {
				if dep, isPkg := c.packageOf(id); isPkg {
					if !exported(sel.Sel) {
						c.diag.errorfAt(ex.Line, ex.Col, "%s is not exported from package %s", sel.Sel, id.Name)
						return terr
					}
					if ct, ok := dep.ctors[sel.Sel]; ok && !dep.prelude[ct.enum] {
						args := c.resolveTypeArgs(ex, fun)
						if args == nil {
							return terr
						}
						return c.callVariantCtor(ex, sel, sel.Sel, dep, ct, args, want)
					}
					c.diag.errorfAt(ex.Line, ex.Col, "undefined: %s.%s", id.Name, sel.Sel)
					return terr
				}
			}
		}
		c.diag.errorfAt(ex.Line, ex.Col, "not a generic constructor call")
		return terr
	case *SelectorExpr:
		if id, ok := fun.X.(*Ident); ok {
			if dep, isPkg := c.packageOf(id); isPkg {
				return c.checkQualifiedCall(ex, fun, id, dep, want)
			}
		}
		xt := c.checkExpr(fun.X)
		if isErr(xt) {
			return terr
		}
		if ch, ok := xt.(*tChan); ok {
			switch fun.Sel {
			case "send":
				if len(ex.Args) != 1 {
					c.diag.errorfAt(ex.Line, ex.Col, "send takes 1 argument")
					return terr
				}
				c.checkAgainst(ex.Args[0], ch.elem)
				return tvoid
			case "recv":
				if len(ex.Args) != 0 {
					c.diag.errorfAt(ex.Line, ex.Col, "recv takes no arguments")
					return terr
				}
				return ch.elem
			case "close":
				if len(ex.Args) != 0 {
					c.diag.errorfAt(ex.Line, ex.Col, "close takes no arguments")
					return terr
				}
				return tvoid
			case "closed":
				c.diag.errorfAt(ex.Line, ex.Col, ".closed() is not supported in v2 (Go channels cannot peek)")
				return terr
			}
			c.diag.errorfAt(ex.Line, ex.Col, "channels have no method %s", fun.Sel)
			return terr
		}
		if en, ok := xt.(*tEnum); ok && en.decl.Name == "Result" {
			if fun.Sel == "IsOk" || fun.Sel == "IsErr" {
				if len(ex.Args) != 0 {
					c.diag.errorfAt(ex.Line, ex.Col, "%s takes no arguments", fun.Sel)
					return terr
				}
				return tbool
			}
		}
		c.diag.errorfAt(ex.Line, ex.Col, "%s has no method %s", xt, fun.Sel)
		return terr
	}
	c.diag.errorfAt(ex.Line, ex.Col, "not callable")
	return terr
}

// resolveTypeArgs resolves the explicit type arguments of a generic
// constructor instantiation; nil means diagnostics were recorded.
func (c *checker) resolveTypeArgs(ex *CallExpr, fun *IndexExpr) []Type {
	var args []Type
	bad := false
	for _, te := range fun.Index {
		tt := exprToType(te)
		if tt == nil {
			c.diag.errorfAt(ex.Line, ex.Col, "invalid type argument")
			bad = true
			continue
		}
		args = append(args, c.resolveType(tt))
	}
	if bad {
		return nil
	}
	return args
}

// checkQualifiedCall checks pkg.Name(args) (§3): an exported function or
// variant constructor of an imported package.
func (c *checker) checkQualifiedCall(ex *CallExpr, fun *SelectorExpr, id *Ident, dep *checker, want Type) Type {
	name := fun.Sel
	if !exported(name) {
		c.diag.errorfAt(ex.Line, ex.Col, "%s is not exported from package %s", name, id.Name)
		return terr
	}
	if ft, ok := dep.funcs[name]; ok {
		c.qualified[fun] = id.Name
		c.checkCallArgs(ex, ft.params)
		if len(ft.results) > 0 {
			return ft.results[0]
		}
		return tvoid
	}
	if ct, ok := dep.ctors[name]; ok && !dep.prelude[ct.enum] {
		return c.callVariantCtor(ex, fun, name, dep, ct, nil, want)
	}
	c.diag.errorfAt(ex.Line, ex.Col, "undefined: %s.%s", id.Name, name)
	for _, a := range ex.Args {
		c.checkExpr(a)
	}
	return terr
}

func (c *checker) callVariantCtor(ex *CallExpr, key Expr, name string, own *checker, ct *ctorTarget, args []Type, want Type) Type {
	if own.ambiguous[name] {
		c.diag.errorfAt(ex.Line, ex.Col, "variant name %s is ambiguous (multiple enums)", name)
		return terr
	}
	if len(ex.Args) != len(ct.variant.Fields) {
		c.diag.errorfAt(ex.Line, ex.Col, "%s takes %d value(s), got %d", name, len(ct.variant.Fields), len(ex.Args))
		return terr
	}
	inferred := false
	if len(ct.enum.TypeParams) > 0 {
		if args == nil {
			// §8-lite: solve type arguments from the expected type and
			// the value arguments — pattern matching, not unification
			args = c.inferTypeArgs(ex, name, own, ct, want)
			if args == nil {
				return terr
			}
			inferred = true
		} else if len(args) != len(ct.enum.TypeParams) { // arity (§5)
			c.diag.errorfAt(ex.Line, ex.Col, "%s takes %d type argument(s), got %d", name, len(ct.enum.TypeParams), len(args))
			return terr
		}
	} else if args != nil {
		c.diag.errorfAt(ex.Line, ex.Col, "%s is not generic", name)
		return terr
	}
	et := &tEnum{decl: ct.enum, args: args}
	for i, f := range ct.variant.Fields {
		ft := own.resolveTypeIn(f.Type, ct.enum)
		if args != nil {
			ft = subst(ft, ct.enum.TypeParams, args)
		}
		if inferred {
			// the args were infer-checked to solve the parameters; now
			// verify them against the solved field types (literals still
			// get adoption + the overflow check)
			if _, ok := c.tryAdopt(ex.Args[i], ft); !ok {
				c.expect(c.types[ex.Args[i]], ft, lineOf(ex.Args[i]), colOf(ex.Args[i]))
			}
		} else {
			c.checkAgainst(ex.Args[i], ft)
		}
	}
	c.resolved[key] = ct
	if inferred {
		c.inferred[key] = args
	}
	return et
}

// inferTypeArgs solves a generic constructor's type arguments without a
// unification engine: the expected type seeds the solution, then each
// value argument's inferred type is pattern-matched against the variant's
// field types. Anything left unsolved is one clear error, not a cascade.
// own is the checker that owns the enum (differs for imported ctors, §3).
func (c *checker) inferTypeArgs(ex *CallExpr, name string, own *checker, ct *ctorTarget, want Type) []Type {
	n := len(ct.enum.TypeParams)
	solved := make([]Type, n)
	// context pins what it can: var r Result[int, string] = Ok(...)
	if en, ok := want.(*tEnum); ok && en.decl == ct.enum && len(en.args) == n {
		copy(solved, en.args)
	}
	for i, f := range ct.variant.Fields {
		at := c.checkExpr(ex.Args[i]) // value arity already verified
		if isErr(at) {
			continue
		}
		ft := own.resolveTypeIn(f.Type, ct.enum)
		c.matchTypePattern(ft, at, ct.enum.TypeParams, solved, lineOf(ex.Args[i]), colOf(ex.Args[i]))
	}
	for i := range solved {
		if solved[i] == nil {
			c.diag.errorfAt(ex.Line, ex.Col, "cannot infer type argument %s for %s; use explicit %s[%s](...)",
				ct.enum.TypeParams[i], name, name, strings.Join(ct.enum.TypeParams, ", "))
			return nil
		}
		solved[i] = defaultType(solved[i])
	}
	return solved
}

// matchTypePattern matches a field type pattern (which may mention the
// enum's type parameters, possibly nested inside enums/maps/slices/chans/
// pointers) against a concrete argument type, recording solutions. A
// conflict diagnoses once and poisons the parameter so downstream checks
// stay silent (§11); an untyped literal constraint yields to a typed one.
func (c *checker) matchTypePattern(pat, arg Type, params []string, solved []Type, line, col int) {
	switch p := pat.(type) {
	case tTypeParam:
		for i, name := range params {
			if name != p.name {
				continue
			}
			switch {
			case solved[i] == nil:
				solved[i] = arg
			case sameType(solved[i], arg):
				// agreement (or either side poisoned): nothing to do
			default:
				if _, ok := arg.(tUntypedInt); ok && isNumeric(solved[i]) {
					return
				}
				if _, ok := arg.(tUntypedFloat); ok && isFloat(solved[i]) {
					return
				}
				if _, ok := solved[i].(tUntypedInt); ok && isNumeric(arg) {
					solved[i] = arg
					return
				}
				if _, ok := solved[i].(tUntypedFloat); ok && isFloat(arg) {
					solved[i] = arg
					return
				}
				c.diag.errorfAt(line, col, "type argument %s inferred as both %s and %s", p.name, solved[i], arg)
				solved[i] = terr // poison: one conflict, one diagnostic
			}
			return
		}
	case *tEnum:
		if a, ok := arg.(*tEnum); ok && a.decl == p.decl && len(a.args) == len(p.args) {
			for i := range p.args {
				c.matchTypePattern(p.args[i], a.args[i], params, solved, line, col)
			}
		}
	case *tMap:
		if a, ok := arg.(*tMap); ok {
			c.matchTypePattern(p.k, a.k, params, solved, line, col)
			c.matchTypePattern(p.v, a.v, params, solved, line, col)
		}
	case *tSlice:
		if a, ok := arg.(*tSlice); ok {
			c.matchTypePattern(p.elem, a.elem, params, solved, line, col)
		}
	case *tChan:
		if a, ok := arg.(*tChan); ok {
			c.matchTypePattern(p.elem, a.elem, params, solved, line, col)
		}
	case *tStar:
		if a, ok := arg.(*tStar); ok {
			c.matchTypePattern(p.x, a.x, params, solved, line, col)
		}
	}
}

func (c *checker) checkCallArgs(ex *CallExpr, params []Type) {
	if len(ex.Args) != len(params) {
		c.diag.errorfAt(ex.Line, ex.Col, "expected %d argument(s), got %d", len(params), len(ex.Args))
		return
	}
	for i := range ex.Args {
		c.checkAgainst(ex.Args[i], params[i])
	}
}

// checkConversion checks T(x) where T is a basic type name (§7). Explicit
// is the whole point: numeric widths mix only through one of these calls.
// string(int) is rejected like Go vet — runes are the currency of text.
func (c *checker) checkConversion(ex *CallExpr, name string) Type {
	to := tBasic{name}
	if len(ex.Args) != 1 {
		c.diag.errorfAt(ex.Line, ex.Col, "conversion to %s takes 1 argument, got %d", name, len(ex.Args))
		for _, a := range ex.Args {
			c.checkExpr(a)
		}
		return terr
	}
	// a literal converts directly, with the overflow check — and only
	// when the target is numeric, so legality is still enforced below
	if t, ok := c.tryAdopt(ex.Args[0], to); ok {
		return t // terr on a failed overflow check: poison, don't re-diagnose
	}
	from := defaultType(c.checkExpr(ex.Args[0]))
	if isErr(from) {
		return terr
	}
	switch {
	case sameType(from, to):
		// identity conversion: redundant but harmless
	case isNumeric(from) && isNumeric(to):
		// numeric <-> numeric, the reason conversions exist
	case (sameType(from, trune) && sameType(to, tstring)) ||
		(sameType(from, tstring) && sameType(to, trune)):
		// rune <-> string
	case sameType(to, tstring):
		c.diag.errorfAt(ex.Line, ex.Col, "cannot convert %s to string (did you mean string(rune(...))?)", from)
		return terr
	default:
		c.diag.errorfAt(ex.Line, ex.Col, "cannot convert %s to %s", from, to)
		return terr
	}
	return to
}

// checkStructLit checks a composite literal against its struct decl:
// keyed fields must exist and match; positional fields go in declaration
// order and must be exactly complete; mixing the two is an error (Go's
// rules). Missing keyed fields take the zero value (SPEC.md).
func (c *checker) checkStructLit(ex *StructLitExpr) Type {
	rt := c.resolveType(ex.Type)
	st, ok := rt.(*tStruct)
	if !ok {
		if !isErr(rt) {
			c.diag.errorfAt(ex.Line, ex.Col, "composite literal of non-struct type %s", rt)
		}
		return terr
	}
	d := st.decl
	seen := map[string]bool{}
	positional := 0
	mixed := false // suppress the positional-count error after a mix (one mistake, one diagnostic)
	for _, fv := range ex.Fields {
		if fv.Name == "" {
			if len(seen) > 0 {
				c.diag.errorfAt(fv.Line, fv.Col, "cannot mix positional and keyed fields")
				mixed = true
				c.checkExpr(fv.Value)
				continue
			}
			if positional >= len(d.Fields) {
				c.diag.errorfAt(fv.Line, fv.Col, "too many values in %s literal (%d fields)", d.Name, len(d.Fields))
				c.checkExpr(fv.Value)
				continue
			}
			c.checkAgainst(fv.Value, c.resolveFieldType(d, d.Fields[positional].Type))
			positional++
			continue
		}
		if positional > 0 {
			c.diag.errorfAt(fv.Line, fv.Col, "cannot mix positional and keyed fields")
			mixed = true
		}
		f := structField(d, fv.Name)
		if f == nil {
			c.diag.errorfAt(fv.Line, fv.Col, "%s has no field %s", d.Name, fv.Name)
			c.checkExpr(fv.Value)
			continue
		}
		if seen[fv.Name] {
			c.diag.errorfAt(fv.Line, fv.Col, "duplicate field %s in literal", fv.Name)
		}
		seen[fv.Name] = true
		c.checkAgainst(fv.Value, c.resolveFieldType(d, f.Type))
	}
	if !mixed && positional > 0 && positional != len(d.Fields) {
		c.diag.errorfAt(ex.Line, ex.Col, "too few values in %s literal: %d of %d fields", d.Name, positional, len(d.Fields))
	}
	return st
}

// resolveFieldType resolves a struct/enum field type using the checker
// that owns the declaration — its TypeExpr may name sibling types that
// only the owning package's namespace knows (§3).
func (c *checker) resolveFieldType(d Decl, te TypeExpr) Type {
	if q := c.declPkg[d]; q != "" {
		return c.imports[q].resolveType(te)
	}
	return c.resolveType(te)
}

func (c *checker) checkSelector(ex *SelectorExpr) Type {
	if id, ok := ex.X.(*Ident); ok {
		if dep, isPkg := c.packageOf(id); isPkg {
			return c.checkQualifiedValue(ex, id, dep)
		}
	}
	xt := c.checkExpr(ex.X)
	if isErr(xt) {
		return terr
	}
	if st, ok := xt.(*tStruct); ok {
		f := structField(st.decl, ex.Sel)
		if f == nil {
			c.diag.errorfAt(ex.Line, ex.Col, "%s has no field %s", xt, ex.Sel)
			return terr
		}
		return c.resolveFieldType(st.decl, f.Type)
	}
	if en, ok := xt.(*tEnum); ok && en.decl.Name == "Result" {
		if ex.Sel == "IsOk" || ex.Sel == "IsErr" {
			return &tFunc{results: []Type{tbool}}
		}
	}
	c.diag.errorfAt(ex.Line, ex.Col, "%s has no field or method %s", xt, ex.Sel)
	return terr
}

// checkQualifiedValue checks pkg.Name in value position (§3): an exported
// function, a unit variant value, or a constructor function value.
func (c *checker) checkQualifiedValue(ex *SelectorExpr, id *Ident, dep *checker) Type {
	name := ex.Sel
	if !exported(name) {
		c.diag.errorfAt(ex.Line, ex.Col, "%s is not exported from package %s", name, id.Name)
		return terr
	}
	if ft, ok := dep.funcs[name]; ok {
		c.qualified[ex] = id.Name
		return ft
	}
	if ct, ok := dep.ctors[name]; ok && !dep.prelude[ct.enum] {
		if dep.ambiguous[name] {
			c.diag.errorfAt(ex.Line, ex.Col, "variant name %s.%s is ambiguous (multiple enums)", id.Name, name)
			return terr
		}
		if len(ct.enum.TypeParams) > 0 {
			c.diag.errorfAt(ex.Line, ex.Col, "%s.%s is generic; use explicit type arguments like %s.%s[..](...)", id.Name, name, id.Name, name)
			return terr
		}
		c.resolved[ex] = ct
		et := &tEnum{decl: ct.enum}
		if len(ct.variant.Fields) == 0 {
			return et // unit variant value
		}
		ft := &tFunc{results: []Type{et}}
		for _, f := range ct.variant.Fields {
			ft.params = append(ft.params, dep.resolveTypeIn(f.Type, ct.enum))
		}
		return ft
	}
	c.diag.errorfAt(ex.Line, ex.Col, "undefined: %s.%s", id.Name, name)
	return terr
}

func (c *checker) checkIndex(ex *IndexExpr) Type {
	// generic instantiation in type position is handled by checkCall;
	// here it's ordinary indexing.
	xt := c.checkExpr(ex.X)
	if isErr(xt) {
		return terr
	}
	if len(ex.Index) != 1 {
		c.diag.errorfAt(ex.Line, ex.Col, "expected 1 index")
		return terr
	}
	switch t := xt.(type) {
	case *tMap:
		c.checkAgainst(ex.Index[0], t.k)
		return t.v
	case *tSlice:
		it := c.checkExpr(ex.Index[0])
		if !isErr(it) && !isNumeric(it) {
			c.diag.errorfAt(ex.Line, ex.Col, "slice index must be a number, got %s", it)
		}
		return t.elem
	}
	c.diag.errorfAt(ex.Line, ex.Col, "cannot index %s", xt)
	return terr
}

// ---------- match ----------

func (c *checker) checkMatch(ex *MatchExpr) Type { return c.checkMatchWant(ex, nil) }

// checkMatchWant checks a match; want != nil is CHECK mode (§6): every
// arm's expression is checked against the expected type, so blame lands on
// the arm that disagrees with the context, not on the first arm.
func (c *checker) checkMatchWant(ex *MatchExpr, want Type) Type {
	hasChan := false
	for _, a := range ex.Arms {
		switch a.Pat.(type) {
		case *RecvPat, *SendPat, *AfterPat, *ClosedPat:
			hasChan = true
		}
	}
	if hasChan {
		return c.checkMatchSelect(ex, want)
	}
	if ex.Subject == nil {
		return c.checkMatchBool(ex, want)
	}
	return c.checkMatchSubject(ex, want)
}

func (c *checker) fieldType(en *tEnum, v *Variant, k int) Type {
	// the owning package resolves the field pattern (§3): foreign enum
	// fields may name types only its own namespace knows
	own := c
	if q := c.declPkg[en.decl]; q != "" {
		own = c.imports[q]
	}
	ft := own.resolveTypeIn(v.Fields[k].Type, en.decl)
	if len(en.args) > 0 {
		ft = subst(ft, en.decl.TypeParams, en.args)
	}
	return ft
}

func findVariant(d *EnumDecl, name string) *Variant {
	for i := range d.Variants {
		if d.Variants[i].Name == name {
			return &d.Variants[i]
		}
	}
	return nil
}

func patLine(p Pattern) int {
	switch pt := p.(type) {
	case *WildcardPat:
		return pt.Line
	case *IdentPat:
		return pt.Line
	case *LiteralPat:
		return pt.Line
	case *VariantPat:
		return pt.Line
	case *RecvPat:
		return pt.Line
	case *SendPat:
		return pt.Line
	case *AfterPat:
		return pt.Line
	case *ClosedPat:
		return pt.Line
	case *BoolPat:
		return pt.Line
	}
	return 0
}

// patCol is patLine for columns (§11 carets on pattern diagnostics).
func patCol(p Pattern) int {
	switch pt := p.(type) {
	case *WildcardPat:
		return pt.Col
	case *IdentPat:
		return pt.Col
	case *LiteralPat:
		return pt.Col
	case *VariantPat:
		return pt.Col
	case *RecvPat:
		return pt.Col
	case *SendPat:
		return pt.Col
	case *AfterPat:
		return pt.Col
	case *ClosedPat:
		return pt.Col
	case *BoolPat:
		return pt.Col
	}
	return 0
}

// checkArmBody checks one arm body; in CHECK mode (want != nil) a
// single-expression arm is verified against the expected type.
func (c *checker) checkArmBody(a *MatchArm, want Type) Type {
	if a.BodyExpr != nil {
		if want != nil && !isErr(want) {
			return c.checkAgainst(a.BodyExpr, want)
		}
		return c.checkExpr(a.BodyExpr)
	}
	if want != nil && !isErr(want) {
		c.diag.errorfAt(a.Line, a.Col, "match in value context must produce a value in every arm")
	}
	c.child()
	c.checkStmts(a.Body)
	c.pop()
	return tvoid
}

// unifyArms merges arm result types (§7): tError and tNever are absorbed
// so a poisoned or diverging arm never masks the real result type.
func (c *checker) unifyArms(cur, next Type, a *MatchArm) Type {
	if cur == nil {
		return next
	}
	if isErr(cur) {
		return next
	}
	if isErr(next) || isNever(next) {
		return cur
	}
	if isNever(cur) {
		return next
	}
	// an untyped literal arm yields to a typed numeric arm (§7)
	if _, ok := cur.(tUntypedInt); ok && isNumeric(next) {
		return next
	}
	if _, ok := next.(tUntypedInt); ok && isNumeric(cur) {
		return cur
	}
	if _, ok := cur.(tUntypedFloat); ok && isFloat(next) {
		return next
	}
	if _, ok := next.(tUntypedFloat); ok && isFloat(cur) {
		return cur
	}
	if !sameType(cur, next) {
		c.diag.errorfAt(a.Line, a.Col, "match arms produce different types (%s vs %s)", cur, next)
	}
	return cur
}

func (c *checker) checkMatchSubject(ex *MatchExpr, want Type) Type {
	st := c.checkExpr(ex.Subject)
	en, isEnum := st.(*tEnum)
	covered := map[string]bool{}
	seenLits := map[string]bool{}  // unguarded basic literals, for duplicate detection
	hasWild := false
	catchAll := false // an unguarded arm above matches everything (§9 usefulness)
	var resultT Type
	for i := range ex.Arms {
		a := &ex.Arms[i]
		c.child()
		unguarded := a.Guard == nil
		switch p := a.Pat.(type) {
		case *WildcardPat:
			if catchAll {
				c.diag.warnf(patLine(p), "unreachable pattern")
			}
			if unguarded {
				hasWild = true
				catchAll = true
			}
		case *VariantPat:
			if !isEnum {
				if !isErr(st) {
					c.diag.errorfAt(patLine(p), patCol(p), "variant pattern %s on non-enum subject %s", p.Name, st)
				}
			} else {
				v := findVariant(en.decl, p.Name)
				if v == nil {
					c.diag.errorfAt(patLine(p), patCol(p), "%s has no variant %s", en, p.Name)
				} else {
					if len(p.Bindings) != len(v.Fields) {
						c.diag.errorfAt(patLine(p), patCol(p), "%s has %d field(s), pattern binds %d", p.Name, len(v.Fields), len(p.Bindings))
					}
					if catchAll || covered[p.Name] {
						c.diag.warnf(patLine(p), "unreachable pattern")
					}
					// guarded arms don't count toward coverage (§9):
					// the guard may fail and control falls through.
					if unguarded {
						covered[p.Name] = true
					}
					for k, bd := range p.Bindings {
						if bd != "_" && k < len(v.Fields) {
							c.cur.vars[bd] = c.fieldType(en, v, k)
						}
					}
				}
			}
		case *IdentPat:
			if isEnum && findVariant(en.decl, p.Name) != nil {
				v := findVariant(en.decl, p.Name)
				if len(v.Fields) > 0 {
					c.diag.errorfAt(patLine(p), patCol(p), "%s carries data; use %s(...) in the pattern", p.Name, p.Name)
				}
				if catchAll || covered[p.Name] {
					c.diag.warnf(patLine(p), "unreachable pattern")
				}
				if unguarded {
					covered[p.Name] = true
				}
				c.patVariant[p] = true
			} else {
				// a binding matches everything, like _ (§9)
				if catchAll {
					c.diag.warnf(patLine(p), "unreachable pattern")
				}
				if unguarded {
					catchAll = true
					hasWild = true
				}
				c.cur.vars[p.Name] = st
			}
		case *LiteralPat:
			lt := c.checkExpr(p.X)
			c.expect(lt, st, patLine(p), patCol(p))
			if catchAll {
				c.diag.warnf(patLine(p), "unreachable pattern")
			}
			if bl, ok := p.X.(*BasicLit); ok && unguarded {
				key := fmt.Sprintf("%d:%s", bl.Kind, bl.Value)
				if seenLits[key] {
					c.diag.warnf(patLine(p), "unreachable pattern (duplicate)")
				}
				seenLits[key] = true
			}
		case *BoolPat:
			c.diag.errorfAt(patLine(p), patCol(p), "'if' arms need a subject-less match")
		default:
			c.diag.errorfAt(patLine(p), patCol(p), "channel arm in a match with a subject")
		}
		if a.Guard != nil {
			gt := c.checkExpr(a.Guard)
			c.expectBool(gt, lineOf(a.Guard), colOf(a.Guard), "guard")
		}
		resultT = c.unifyArms(resultT, c.checkArmBody(a, want), a)
		c.pop()
	}
	if isEnum && !hasWild {
		var missing []string
		for _, v := range en.decl.Variants {
			if !covered[v.Name] {
				missing = append(missing, v.Name)
			}
		}
		if len(missing) > 0 {
			c.diag.errorfAt(ex.Line, ex.Col, "non-exhaustive match on %s: missing %s", en, strings.Join(missing, ", "))
		}
	}
	if !isEnum && !hasWild && !isErr(st) {
		c.diag.errorfAt(ex.Line, ex.Col, "match on %s needs a _ arm", st)
	}
	if resultT == nil {
		resultT = tvoid
	}
	return resultT
}

func (c *checker) checkMatchSelect(ex *MatchExpr, want Type) Type {
	if ex.Subject != nil {
		c.diag.errorfAt(ex.Line, ex.Col, "channel arms need a subject-less match")
	}
	var resultT Type
	for i := range ex.Arms {
		a := &ex.Arms[i]
		c.child()
		switch p := a.Pat.(type) {
		case *RecvPat:
			ct := c.checkExpr(p.Chan)
			if ch, ok := ct.(*tChan); ok {
				if p.Bind != "" && p.Bind != "_" {
					c.cur.vars[p.Bind] = ch.elem
				}
			} else if !isErr(ct) {
				c.diag.errorfAt(patLine(p), patCol(p), "recv on non-channel %s", ct)
			}
		case *SendPat:
			ct := c.checkExpr(p.Chan)
			if ch, ok := ct.(*tChan); ok {
				c.checkAgainst(p.Value, ch.elem)
			} else if !isErr(ct) {
				c.diag.errorfAt(patLine(p), patCol(p), "send on non-channel %s", ct)
			}
		case *AfterPat:
			dt := c.checkExpr(p.D)
			if !isErr(dt) && !isNumeric(dt) {
				c.diag.errorfAt(patLine(p), patCol(p), "after() duration must be numeric, got %s", dt)
			}
		case *ClosedPat:
			c.diag.errorfAt(patLine(p), patCol(p), ".closed() arms are not supported in v2 (Go channels cannot peek)")
		case *WildcardPat:
		default:
			c.diag.errorfAt(patLine(p), patCol(p), "cannot mix channel arms with value/enum arms")
		}
		if a.Guard != nil {
			c.diag.errorfAt(a.Line, a.Col, "guards on channel arms are not supported in v2")
		}
		resultT = c.unifyArms(resultT, c.checkArmBody(a, want), a)
		c.pop()
	}
	if resultT == nil {
		resultT = tvoid
	}
	return resultT
}

func (c *checker) checkMatchBool(ex *MatchExpr, want Type) Type {
	var resultT Type
	catchAll := false
	for i := range ex.Arms {
		a := &ex.Arms[i]
		c.child()
		switch p := a.Pat.(type) {
		case *BoolPat:
			if catchAll {
				c.diag.warnf(patLine(p), "unreachable pattern")
			}
			bt := c.checkExpr(p.X)
			c.expectBool(bt, lineOf(p.X), colOf(p.X), "boolean arm")
		case *WildcardPat:
			if catchAll {
				c.diag.warnf(patLine(p), "unreachable pattern")
			}
			catchAll = true
		default:
			c.diag.errorfAt(patLine(p), patCol(p), "subject-less match arms must be channel patterns, 'if' conditions, or '_'")
		}
		if a.Guard != nil {
			c.diag.errorfAt(a.Line, a.Col, "guards on boolean arms are not supported")
		}
		resultT = c.unifyArms(resultT, c.checkArmBody(a, want), a)
		c.pop()
	}
	if resultT == nil {
		resultT = tvoid
	}
	return resultT
}

// ---------- helpers for diagnostics from other passes ----------

// diagFromError folds a lex/parse error (format "line N: msg") into the
// diagnostics collection.
func diagFromError(d *Diagnostics, err error) {
	line, msg := splitLinePrefix(err.Error())
	d.add(sevErr, line, msg)
}

func splitLinePrefix(msg string) (int, string) {
	const p = "line "
	if !strings.HasPrefix(msg, p) {
		return 0, msg
	}
	rest := msg[len(p):]
	i := strings.Index(rest, ": ")
	if i < 0 {
		return 0, msg
	}
	var n int
	if _, err := fmt.Sscanf(rest[:i], "%d", &n); err != nil {
		return 0, msg
	}
	return n, rest[i+2:]
}
