package main

// meta.go — §10 comptime metaprogramming.
//
// Top-level `comptime { ... }` blocks run inside checkImports BEFORE any
// name registration or type resolution, so every mutation they make is
// what the rest of the pipeline sees. The model is live handles, not
// snapshots: a ckRecord wraps a real AST node with get/set closures per
// field, so
//
//	d.name = d.name + "bar"
//	d.params.add(Param("bar", "int"))
//	d.variants.add(Variant("Neon"))
//	gen(Enum("Mood"))
//
// rewrite the actual declarations. Builtins:
//
//	decls()                    list of the package's declarations
//	Param(name, type)          a func parameter (Field/Variant similar)
//	Enum(name)/Struct(name)    a new, empty type declaration
//	Func(name)                 a new function (set .body to source text)
//	gen(decl)                  inject a built declaration into the package
//	len(x) / str(x) / print(...)  utilities (print -> stderr, like @compileLog)
//	split/join/upper/lower/replace/contains/has_prefix/has_suffix/repeat/trim
//	                           string utilities for codegen
//
// Match expressions also evaluate: literal/wildcard/binding/bool arms with
// guards, expression bodies only, no variant or channel patterns.
//
// Field access on records: .kind .name (read/write), .params .results
// .fields .variants (live lists with .add), .type (read/write, source
// text), .body (func source text, read/write).

import (
	"fmt"
	"io"
	"math/big"
	"os"
	"strconv"
	"strings"
)

// metaOut is where comptime print writes (stderr, like Zig's @compileLog);
// tests swap it for a buffer.
var metaOut io.Writer = os.Stderr

// metaRecord is a live handle onto an AST node.
type metaRecord struct {
	what   string // "FuncDecl", "EnumDecl", "StructDecl", "Field", "Variant"
	node   any    // the wrapped node (*FuncDecl, *EnumDecl, *StructDecl, *Field, *Variant)
	fields []metaField
}

type metaField struct {
	name string
	get  func() constVal
	set  func(v constVal) error // nil = read-only
}

func (r *metaRecord) field(name string) *metaField {
	for i := range r.fields {
		if r.fields[i].name == name {
			return &r.fields[i]
		}
	}
	return nil
}

func strVal(s string) constVal      { return constVal{kind: ckString, s: s} }
func recVal(r *metaRecord) constVal { return constVal{kind: ckRecord, r: r} }

func listVal(l []constVal, add func(constVal) error) constVal {
	return constVal{kind: ckList, l: l, add: add}
}

// metaString renders a comptime value for print and str().
func metaString(v constVal) string {
	switch v.kind {
	case ckInt, ckRune, ckDuration:
		return v.i.String()
	case ckFloat:
		return strconv.FormatFloat(v.f, 'g', -1, 64)
	case ckString:
		return v.s
	case ckBool:
		if v.b {
			return "true"
		}
		return "false"
	case ckList:
		parts := make([]string, len(v.l))
		for i, e := range v.l {
			parts[i] = metaString(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case ckRecord:
		parts := make([]string, len(v.r.fields))
		for i, f := range v.r.fields {
			parts[i] = f.name + ": " + metaString(f.get())
		}
		return v.r.what + "{" + strings.Join(parts, ", ") + "}"
	case ckVoid:
		return "void"
	}
	return "<?>"
}

// ---------- live handles onto declarations ----------

// typeExprString renders a TypeExpr back to go++ source (for .type getters).
func typeExprString(te TypeExpr) string {
	switch t := te.(type) {
	case *IdentType:
		return t.Name
	case *IndexType:
		parts := make([]string, len(t.Args))
		for i, a := range t.Args {
			parts[i] = typeExprString(a)
		}
		return typeExprString(t.X) + "[" + strings.Join(parts, ", ") + "]"
	case *MapType:
		return "map<" + typeExprString(t.K) + ", " + typeExprString(t.V) + ">"
	case *ChanType:
		return "chan[" + typeExprString(t.Elem) + "]"
	case *SliceType:
		return "[]" + typeExprString(t.Elem)
	case *StarType:
		return "*" + typeExprString(t.X)
	}
	return "?"
}

func validIdentName(s string) bool {
	if s == "" || !isAlpha(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isAlpha(s[i]) && !isDigit(s[i]) && s[i] != '_' {
			return false
		}
	}
	return true
}

func nameField(p *string) metaField {
	return metaField{
		name: "name",
		get:  func() constVal { return strVal(*p) },
		set: func(v constVal) error {
			if v.kind != ckString || !validIdentName(v.s) {
				return fmt.Errorf("name must be an identifier string, got %s", metaString(v))
			}
			*p = v.s
			return nil
		},
	}
}

// wrapField exposes a Field (struct field, func param/result, variant
// payload member) with read/write .name and .type.
func (mc *metaCtx) wrapField(f *Field) constVal {
	r := &metaRecord{what: "Field", node: f}
	r.fields = append(r.fields, nameField(&f.Name))
	r.fields = append(r.fields, metaField{
		name: "type",
		get:  func() constVal { return strVal(typeExprString(f.Type)) },
		set: func(v constVal) error {
			te, err := mc.typeArg(v)
			if err != nil {
				return err
			}
			f.Type = te
			return nil
		},
	})
	return recVal(r)
}

// wrapFieldList exposes a *[]Field as a live list: reading materializes
// handles by index, .add appends a Field record built by Param/Field.
func (mc *metaCtx) wrapFieldList(fs *[]Field) constVal {
	elems := make([]constVal, len(*fs))
	for i := range *fs {
		i := i // index handles stay valid across appends
		elems[i] = mc.wrapField(&(*fs)[i])
	}
	add := func(v constVal) error {
		if v.kind != ckRecord {
			return fmt.Errorf("expected a Field record (from Param/Field), got %s", metaString(v))
		}
		f, ok := v.r.node.(*Field)
		if !ok {
			return fmt.Errorf("expected a Field record (from Param/Field), got %s", metaString(v))
		}
		*fs = append(*fs, *f)
		return nil
	}
	return listVal(elems, add)
}

func (mc *metaCtx) wrapVariant(v *Variant) constVal {
	r := &metaRecord{what: "Variant", node: v}
	r.fields = append(r.fields, nameField(&v.Name))
	r.fields = append(r.fields, metaField{
		name: "fields",
		get:  func() constVal { return mc.wrapFieldList(&v.Fields) },
	})
	return recVal(r)
}

func (mc *metaCtx) wrapFunc(fn *FuncDecl) constVal {
	r := &metaRecord{what: "FuncDecl", node: fn}
	r.fields = append(r.fields,
		metaField{name: "kind", get: func() constVal { return strVal("func") }},
		nameField(&fn.Name),
		metaField{name: "params", get: func() constVal { return mc.wrapFieldList(&fn.Params) }},
		metaField{name: "results", get: func() constVal { return mc.wrapFieldList(&fn.Results) }},
		metaField{
			name: "body",
			get:  func() constVal { return strVal(mc.bodyText(fn)) },
			set: func(v constVal) error {
				if v.kind != ckString {
					return fmt.Errorf("body must be source text, got %s", metaString(v))
				}
				b, err := parseBodyText(v.s)
				if err != nil {
					return err
				}
				fn.Body = b
				return nil
			},
		},
	)
	return recVal(r)
}

func (mc *metaCtx) wrapEnum(e *EnumDecl) constVal {
	r := &metaRecord{what: "EnumDecl", node: e}
	r.fields = append(r.fields,
		metaField{name: "kind", get: func() constVal { return strVal("enum") }},
		nameField(&e.Name),
		metaField{
			name: "variants",
			get: func() constVal {
				elems := make([]constVal, len(e.Variants))
				for i := range e.Variants {
					i := i
					elems[i] = mc.wrapVariant(&e.Variants[i])
				}
				add := func(v constVal) error {
					if v.kind != ckRecord {
						return fmt.Errorf("expected a Variant record, got %s", metaString(v))
					}
					vv, ok := v.r.node.(*Variant)
					if !ok {
						return fmt.Errorf("expected a Variant record, got %s", metaString(v))
					}
					e.Variants = append(e.Variants, *vv)
					return nil
				}
				return listVal(elems, add)
			},
		},
	)
	return recVal(r)
}

func (mc *metaCtx) wrapStruct(s *StructDecl) constVal {
	r := &metaRecord{what: "StructDecl", node: s}
	r.fields = append(r.fields,
		metaField{name: "kind", get: func() constVal { return strVal("struct") }},
		nameField(&s.Name),
		metaField{name: "fields", get: func() constVal { return mc.wrapFieldList(&s.Fields) }},
	)
	return recVal(r)
}

func (mc *metaCtx) wrapDecl(d Decl) constVal {
	switch dd := d.(type) {
	case *FuncDecl:
		return mc.wrapFunc(dd)
	case *EnumDecl:
		return mc.wrapEnum(dd)
	case *StructDecl:
		return mc.wrapStruct(dd)
	}
	return constVal{}
}

// ---------- text -> AST helpers ----------

// parseTypeText parses a type from source text ("int", "[]foo.Status").
func parseTypeText(s string) (te TypeExpr, err error) {
	toks, lerr := lex(s)
	if lerr != nil {
		return nil, lerr
	}
	p := &parser{toks: toks, diag: &Diagnostics{}}
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(parseError); ok {
				err = fmt.Errorf("bad type %q: %s", s, pe.msg)
			} else {
				panic(r)
			}
		}
	}()
	te = p.parseType()
	if p.cur().kind != kEOF {
		return nil, fmt.Errorf("bad type %q: trailing %q", s, p.cur().text)
	}
	return te, nil
}

// parseBodyText parses statement source text into a Block (for .body set).
func parseBodyText(s string) (b *Block, err error) {
	toks, lerr := lex("{\n" + s + "\n}")
	if lerr != nil {
		return nil, lerr
	}
	p := &parser{toks: toks, diag: &Diagnostics{}}
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(parseError); ok {
				err = fmt.Errorf("bad body: %s", pe.msg)
			} else {
				panic(r)
			}
		}
	}()
	return p.parseBlock(), nil
}

// bodyText slices a function's body out of the package source by brace
// matching from its declaration line (for .body get).
func (mc *metaCtx) bodyText(fn *FuncDecl) string {
	src := mc.c.src
	if src == "" || fn.Line <= 0 {
		return "<unavailable>"
	}
	lines := strings.Split(src, "\n")
	var b strings.Builder
	depth := 0
	started := false
	for i := fn.Line - 1; i < len(lines); i++ {
		line := lines[i]
		if started {
			b.WriteByte('\n')
		}
		for j := 0; j < len(line); j++ {
			switch line[j] {
			case '{':
				depth++
				started = true
			case '}':
				depth--
			}
		}
		b.WriteString(line)
		if started && depth == 0 {
			return b.String()
		}
	}
	return b.String()
}

// ---------- the interpreter ----------

type metaCtx struct {
	c         *checker
	file      *File
	fuel      *int
	depth     int  // comptime call depth (recursion cap)
	inFunc    bool // executing a comptime-called function's body
	loopDepth int  // inside a comptime loop (for break legality)
}

// metaFlow carries control signals up the statement interpreter: a
// return from a comptime-called function, or a break out of a loop.
type metaFlow struct {
	val      constVal
	returned bool
	broke    bool
}

// evalComptimeDecls runs every top-level comptime block in source order.
// It is called before any registration or resolution, so mutations and
// gen'd declarations are what the rest of sema sees. Variables persist
// across blocks: later comptime blocks use what earlier ones declared.
func (c *checker) evalComptimeDecls(f *File) {
	has := false
	for _, d := range f.Decls {
		if _, ok := d.(*ComptimeDecl); ok {
			has = true
			break
		}
	}
	if !has {
		return
	}
	fuel := constFuelLimit
	mc := &metaCtx{c: c, file: f, fuel: &fuel}
	env := map[string]constVal{}
	for _, d := range f.Decls {
		cd, ok := d.(*ComptimeDecl)
		if !ok {
			continue
		}
		if fl, _ := mc.execStmts(cd.Body.List, env); fl.returned {
			c.diag.errorfAt(cd.Line, cd.Col, "return outside a comptime function")
		}
	}
}

func (mc *metaCtx) tick(line int) bool {
	*mc.fuel--
	if *mc.fuel <= 0 {
		mc.fail(line, "comptime evaluation limit reached")
		return false
	}
	return true
}

func (mc *metaCtx) fail(line int, format string, args ...any) (constVal, bool) {
	mc.c.diag.errorf(line, format, args...)
	return constVal{}, false
}

func (mc *metaCtx) execStmts(list []Stmt, env map[string]constVal) (metaFlow, bool) {
	for _, s := range list {
		if !mc.tick(stmtLine(s)) {
			return metaFlow{}, false
		}
		fl, ok := mc.execStmt(s, env)
		if !ok || fl.returned || fl.broke {
			return fl, ok
		}
	}
	return metaFlow{}, true
}

func (mc *metaCtx) execStmt(s Stmt, env map[string]constVal) (metaFlow, bool) {
	switch st := s.(type) {
	case *ExprStmt:
		_, ok := mc.evalExpr(st.X, env)
		return metaFlow{}, ok
	case *VarStmt:
		if st.Init == nil {
			mc.fail(st.Line, "comptime variables need an initializer")
			return metaFlow{}, false
		}
		if v, ok := mc.evalExpr(st.Init, env); ok {
			env[st.Name] = v
		}
		return metaFlow{}, true
	case *AssignStmt:
		return mc.execAssign(st, env), true
	case *IfStmt:
		cv, ok := mc.evalExpr(st.Cond, env)
		if !ok {
			return metaFlow{}, false
		}
		if cv.kind != ckBool {
			mc.fail(st.Line, "comptime if condition must be bool, got %s", metaString(cv))
			return metaFlow{}, false
		}
		if cv.b {
			return mc.execStmts(st.Then.List, env)
		}
		switch els := st.Else.(type) {
		case *Block:
			return mc.execStmts(els.List, env)
		case *IfStmt:
			return mc.execStmt(els, env)
		}
		return metaFlow{}, true
	case *ForInStmt:
		if st.Var2 != "" {
			mc.fail(st.Line, "index bindings are not supported in comptime for-in")
			return metaFlow{}, false
		}
		lv, ok := mc.evalExpr(st.X, env)
		if !ok {
			return metaFlow{}, false
		}
		if lv.kind != ckList {
			mc.fail(st.Line, "for-in needs a list, got %s", metaString(lv))
			return metaFlow{}, false
		}
		mc.loopDepth++
		for _, elem := range lv.l {
			env[st.Var] = elem
			fl, ok := mc.execStmts(st.Body.List, env)
			if !ok || fl.returned {
				mc.loopDepth--
				return fl, ok
			}
			if fl.broke {
				break
			}
		}
		mc.loopDepth--
		return metaFlow{}, true
	case *ForStmt:
		return mc.execFor(st, env)
	case *LoopStmt:
		mc.loopDepth++
		for {
			if !mc.tick(st.Line) {
				mc.loopDepth--
				return metaFlow{}, false
			}
			fl, ok := mc.execStmts(st.Body.List, env)
			if !ok || fl.returned {
				mc.loopDepth--
				return fl, ok
			}
			if fl.broke {
				break
			}
		}
		mc.loopDepth--
		return metaFlow{}, true
	case *BreakStmt:
		if mc.loopDepth == 0 {
			mc.fail(st.Line, "break outside a comptime loop")
			return metaFlow{}, false
		}
		if st.Label != "" {
			mc.fail(st.Line, "labeled break is not supported in comptime")
			return metaFlow{}, false
		}
		return metaFlow{broke: true}, true
	case *ReturnStmt:
		if !mc.inFunc {
			mc.fail(st.Line, "return outside a comptime function")
			return metaFlow{}, false
		}
		if len(st.Results) > 1 {
			mc.fail(st.Line, "comptime functions return at most 1 value")
			return metaFlow{}, false
		}
		if len(st.Results) == 0 {
			return metaFlow{returned: true}, true
		}
		v, ok := mc.evalExpr(st.Results[0], env)
		if !ok {
			return metaFlow{}, false
		}
		return metaFlow{val: v, returned: true}, true
	case *IncDecStmt:
		id, ok := st.X.(*Ident)
		if !ok {
			mc.fail(st.Line, "unsupported %s target in comptime", st.Op)
			return metaFlow{}, false
		}
		cur, ok := env[id.Name]
		if !ok {
			mc.fail(st.Line, "undefined comptime name: %s", id.Name)
			return metaFlow{}, false
		}
		op := "+"
		if st.Op == "--" {
			op = "-"
		}
		v, ok := mc.c.constBinary(&BinaryExpr{Op: op, Line: st.Line}, cur, intVal(big.NewInt(1)))
		if !ok {
			return metaFlow{}, false
		}
		env[id.Name] = v
		return metaFlow{}, true
	case *Block:
		return mc.execStmts(st.List, env)
	}
	mc.fail(stmtLine(s), "statement not supported in comptime")
	return metaFlow{}, false
}

func (mc *metaCtx) execAssign(st *AssignStmt, env map[string]constVal) metaFlow {
	if len(st.Lhs) != 1 || len(st.Rhs) != 1 {
		mc.fail(st.Line, "unsupported assignment in comptime")
		return metaFlow{}
	}
	v, ok := mc.evalExpr(st.Rhs[0], env)
	if !ok {
		return metaFlow{}
	}
	switch lhs := st.Lhs[0].(type) {
	case *Ident:
		switch st.Op {
		case "=", ":=":
			env[lhs.Name] = v
		case "+=", "-=", "*=", "/=", "%=":
			cur, ok := env[lhs.Name]
			if !ok {
				mc.fail(st.Line, "undefined comptime name: %s", lhs.Name)
				return metaFlow{}
			}
			nv, ok := mc.c.constBinary(&BinaryExpr{Op: st.Op[:1], Line: st.Line}, cur, v)
			if !ok {
				return metaFlow{}
			}
			env[lhs.Name] = nv
		default:
			mc.fail(st.Line, "unsupported assignment op %s in comptime", st.Op)
		}
	case *SelectorExpr:
		if st.Op != "=" {
			mc.fail(st.Line, "unsupported assignment op %s on a record field", st.Op)
			return metaFlow{}
		}
		rv, ok := mc.evalExpr(lhs.X, env)
		if !ok {
			return metaFlow{}
		}
		if rv.kind != ckRecord {
			mc.fail(st.Line, "cannot set a field on %s", metaString(rv))
			return metaFlow{}
		}
		f := rv.r.field(lhs.Sel)
		if f == nil {
			mc.fail(st.Line, "%s has no field %s", rv.r.what, lhs.Sel)
			return metaFlow{}
		}
		if f.set == nil {
			mc.fail(st.Line, "%s.%s is read-only", rv.r.what, lhs.Sel)
			return metaFlow{}
		}
		if err := f.set(v); err != nil {
			mc.fail(st.Line, "%s", err)
		}
	default:
		mc.fail(st.Line, "unsupported assignment target in comptime")
	}
	return metaFlow{}
}

// execFor runs a Go-style for statement at comptime.
func (mc *metaCtx) execFor(st *ForStmt, env map[string]constVal) (metaFlow, bool) {
	if st.Init != nil {
		if fl, ok := mc.execStmt(st.Init, env); !ok || fl.returned || fl.broke {
			return fl, ok
		}
	}
	mc.loopDepth++
	defer func() { mc.loopDepth-- }()
	for {
		if !mc.tick(st.Line) {
			return metaFlow{}, false
		}
		if st.Cond != nil {
			cv, ok := mc.evalExpr(st.Cond, env)
			if !ok {
				return metaFlow{}, false
			}
			if cv.kind != ckBool {
				mc.fail(st.Line, "comptime for condition must be bool, got %s", metaString(cv))
				return metaFlow{}, false
			}
			if !cv.b {
				return metaFlow{}, true
			}
		}
		fl, ok := mc.execStmts(st.Body.List, env)
		if !ok || fl.returned {
			return fl, ok
		}
		if fl.broke {
			return metaFlow{}, true
		}
		if st.Post != nil {
			if fl, ok := mc.execStmt(st.Post, env); !ok || fl.returned || fl.broke {
				return fl, ok
			}
		}
	}
}

// findDecl looks up a package declaration by name for bare-name handles.
func (mc *metaCtx) findDecl(name string) Decl {
	for _, d := range mc.file.Decls {
		switch dd := d.(type) {
		case *FuncDecl:
			if dd.Name == name {
				return dd
			}
		case *EnumDecl:
			if dd.Name == name {
				return dd
			}
		case *StructDecl:
			if dd.Name == name {
				return dd
			}
		}
	}
	return nil
}

// callFunc interprets a previously declared function at comptime: args
// bind to a fresh env, the body runs under the same statement engine,
// fuel and a depth cap keep recursion honest.
func (mc *metaCtx) callFunc(fn *FuncDecl, args []constVal, line int) (constVal, bool) {
	if len(args) != len(fn.Params) {
		return mc.fail(line, "%s takes %d argument(s), got %d", fn.Name, len(fn.Params), len(args))
	}
	if mc.depth >= 128 {
		return mc.fail(line, "comptime call depth limit reached (recursion?)")
	}
	env := map[string]constVal{}
	for i, p := range fn.Params {
		env[p.Name] = args[i]
	}
	mc.depth++
	saveF, saveL := mc.inFunc, mc.loopDepth
	mc.inFunc, mc.loopDepth = true, 0
	fl, ok := mc.execStmts(fn.Body.List, env)
	mc.inFunc, mc.loopDepth = saveF, saveL
	mc.depth--
	if !ok {
		return constVal{}, false
	}
	if len(fn.Results) == 0 {
		return constVal{kind: ckVoid}, true
	}
	if !fl.returned {
		return mc.fail(line, "comptime call to %s did not return", fn.Name)
	}
	return fl.val, true
}

func (mc *metaCtx) evalExpr(x Expr, env map[string]constVal) (constVal, bool) {
	if !mc.tick(lineOf(x)) {
		return constVal{}, false
	}
	switch ex := x.(type) {
	case *BasicLit:
		return mc.c.constEval(ex, mc.fuel) // literals need no scope
	case *Ident:
		if v, ok := env[ex.Name]; ok {
			return v, true
		}
		// previously declared things are usable bare: Color is the enum
		// handle, greet the func handle (called via evalCall)
		if d := mc.findDecl(ex.Name); d != nil {
			return mc.wrapDecl(d), true
		}
		return mc.fail(ex.Line, "undefined comptime name: %s", ex.Name)
	case *UnaryExpr:
		v, ok := mc.evalExpr(ex.X, env)
		if !ok {
			return constVal{}, false
		}
		return mc.c.constUnary(ex, v)
	case *BinaryExpr:
		l, ok := mc.evalExpr(ex.X, env)
		if !ok {
			return constVal{}, false
		}
		r, ok := mc.evalExpr(ex.Y, env)
		if !ok {
			return constVal{}, false
		}
		return mc.c.constBinary(ex, l, r)
	case *SelectorExpr:
		rv, ok := mc.evalExpr(ex.X, env)
		if !ok {
			return constVal{}, false
		}
		if rv.kind != ckRecord {
			return mc.fail(ex.Line, "%s has no field %s", metaString(rv), ex.Sel)
		}
		f := rv.r.field(ex.Sel)
		if f == nil {
			return mc.fail(ex.Line, "%s has no field %s", rv.r.what, ex.Sel)
		}
		return f.get(), true
	case *IndexExpr:
		lv, ok := mc.evalExpr(ex.X, env)
		if !ok {
			return constVal{}, false
		}
		if lv.kind != ckList {
			return mc.fail(ex.Line, "cannot index %s", metaString(lv))
		}
		if len(ex.Index) != 1 {
			return mc.fail(ex.Line, "expected 1 index")
		}
		iv, ok := mc.evalExpr(ex.Index[0], env)
		if !ok {
			return constVal{}, false
		}
		if iv.kind != ckInt || !iv.i.IsInt64() {
			return mc.fail(ex.Line, "list index must be an integer")
		}
		n := iv.i.Int64()
		if n < 0 || n >= int64(len(lv.l)) {
			return mc.fail(ex.Line, "list index %d out of range (%d elements)", n, len(lv.l))
		}
		return lv.l[n], true
	case *CallExpr:
		return mc.evalCall(ex, env)
	case *MatchExpr:
		return mc.evalMatch(ex, env)
	}
	return mc.fail(lineOf(x), "not a comptime expression")
}

// evalMatch interprets a comptime match: arms are tried in order and the
// first whose pattern matches (with a true guard) yields the value. Arm
// bodies must be expressions; there are no enum values at comptime, so
// variant and channel patterns are diagnosed, and an unmatched subject is
// a compile error rather than a runtime panic.
func (mc *metaCtx) evalMatch(ex *MatchExpr, env map[string]constVal) (constVal, bool) {
	var subj constVal
	if ex.Subject != nil {
		v, ok := mc.evalExpr(ex.Subject, env)
		if !ok {
			return constVal{}, false
		}
		subj = v
	}
	for _, a := range ex.Arms {
		matched, ok := mc.matchArm(ex, a, subj, env)
		if !ok {
			return constVal{}, false
		}
		if !matched {
			continue
		}
		if a.Guard != nil {
			g, ok := mc.evalExpr(a.Guard, env)
			if !ok {
				return constVal{}, false
			}
			if g.kind != ckBool {
				return mc.fail(a.Line, "comptime match guard must be bool, got %s", metaString(g))
			}
			if !g.b {
				continue
			}
		}
		if a.BodyExpr == nil {
			return mc.fail(a.Line, "comptime match arms must be expressions")
		}
		return mc.evalExpr(a.BodyExpr, env)
	}
	return mc.fail(ex.Line, "non-exhaustive comptime match")
}

// matchArm reports whether one arm's pattern matches; bindings land in env
// before the guard runs, so guards can use them.
func (mc *metaCtx) matchArm(ex *MatchExpr, a MatchArm, subj constVal, env map[string]constVal) (bool, bool) {
	switch p := a.Pat.(type) {
	case *WildcardPat:
		return true, true
	case *IdentPat:
		if ex.Subject == nil {
			mc.fail(p.Line, "binding pattern needs a match subject")
			return false, false
		}
		if p.Name != "_" {
			env[p.Name] = subj
		}
		return true, true
	case *LiteralPat:
		if ex.Subject == nil {
			mc.fail(p.Line, "literal pattern needs a match subject")
			return false, false
		}
		v, ok := mc.evalExpr(p.X, env)
		if !ok {
			return false, false
		}
		// a kind mismatch just doesn't match (numeric kinds cross-compare)
		if subj.kind != v.kind && !(isConstNum(subj) && isConstNum(v)) {
			return false, true
		}
		if !isConstNum(subj) && subj.kind != ckString && subj.kind != ckBool {
			mc.fail(a.Line, "cannot match %s against a literal", metaString(subj))
			return false, false
		}
		eq, ok := mc.c.constBinary(&BinaryExpr{Op: "==", Line: a.Line}, subj, v)
		if !ok {
			return false, false
		}
		return eq.b, true
	case *BoolPat:
		v, ok := mc.evalExpr(p.X, env)
		if !ok {
			return false, false
		}
		if v.kind != ckBool {
			mc.fail(p.Line, "bool arm needs a bool condition, got %s", metaString(v))
			return false, false
		}
		return v.b, true
	case *VariantPat:
		mc.fail(p.Line, "variant patterns are not supported in comptime match")
		return false, false
	}
	mc.fail(a.Line, "channel patterns are not supported in comptime")
	return false, false
}

// typeArg converts a comptime value to a TypeExpr: source text, or a
// record wrapping an enum/struct declaration.
func (mc *metaCtx) typeArg(v constVal) (TypeExpr, error) {
	switch v.kind {
	case ckString:
		return parseTypeText(v.s)
	case ckRecord:
		switch n := v.r.node.(type) {
		case *EnumDecl:
			return &IdentType{Name: n.Name}, nil
		case *StructDecl:
			return &IdentType{Name: n.Name}, nil
		}
	}
	return nil, fmt.Errorf("expected a type (source text or a type record), got %s", metaString(v))
}

func (mc *metaCtx) evalCall(ex *CallExpr, env map[string]constVal) (constVal, bool) {
	// method call: list.add(x)
	if sel, ok := ex.Fun.(*SelectorExpr); ok && sel.Sel == "add" {
		lv, ok := mc.evalExpr(sel.X, env)
		if !ok {
			return constVal{}, false
		}
		if lv.kind != ckList || lv.add == nil {
			return mc.fail(ex.Line, "%s has no add method", metaString(lv))
		}
		if len(ex.Args) != 1 {
			return mc.fail(ex.Line, "add takes 1 argument")
		}
		v, ok := mc.evalExpr(ex.Args[0], env)
		if !ok {
			return constVal{}, false
		}
		if err := lv.add(v); err != nil {
			return mc.fail(ex.Line, "%s", err)
		}
		return boolVal(true), true
	}
	id, ok := ex.Fun.(*Ident)
	if !ok {
		return mc.fail(ex.Line, "not a comptime function")
	}
	args := make([]constVal, len(ex.Args))
	for i, a := range ex.Args {
		v, ok := mc.evalExpr(a, env)
		if !ok {
			return constVal{}, false
		}
		args[i] = v
	}
	switch id.Name {
	case "print":
		parts := make([]string, len(args))
		for i, a := range args {
			parts[i] = metaString(a)
		}
		fmt.Fprintln(metaOut, strings.Join(parts, " "))
		return boolVal(true), true
	case "decls":
		var elems []constVal
		for _, d := range mc.file.Decls {
			switch d.(type) {
			case *ComptimeDecl, *BehaviorDecl, *ImplDecl:
				continue // behaviors/impls get handles when §8 metaprogramming lands
			}
			elems = append(elems, mc.wrapDecl(d))
		}
		return listVal(elems, nil), true
	case "gen":
		if len(args) != 1 || args[0].kind != ckRecord {
			return mc.fail(ex.Line, "gen takes a declaration record (Enum/Struct/Func)")
		}
		d, ok := args[0].r.node.(Decl)
		if !ok {
			return mc.fail(ex.Line, "gen takes a declaration record, got %s", args[0].r.what)
		}
		mc.file.Decls = append(mc.file.Decls, d)
		return boolVal(true), true
	case "Param", "Field":
		if len(args) != 2 {
			return mc.fail(ex.Line, "%s takes (name, type)", id.Name)
		}
		// "" is the unnamed field (results and variant payloads use it)
		if args[0].kind != ckString || (args[0].s != "" && !validIdentName(args[0].s)) {
			return mc.fail(ex.Line, "%s name must be an identifier string", id.Name)
		}
		te, err := mc.typeArg(args[1])
		if err != nil {
			return mc.fail(ex.Line, "%s", err)
		}
		return mc.wrapField(&Field{Name: args[0].s, Type: te, Line: ex.Line}), true
	case "Variant":
		if len(args) < 1 || args[0].kind != ckString || !validIdentName(args[0].s) {
			return mc.fail(ex.Line, "Variant takes a name string")
		}
		v := &Variant{Name: args[0].s, Line: ex.Line}
		if len(args) == 2 {
			if args[1].kind != ckList {
				return mc.fail(ex.Line, "Variant fields must be a list of Field records")
			}
			for _, fv := range args[1].l {
				if fv.kind != ckRecord {
					return mc.fail(ex.Line, "Variant fields must be Field records")
				}
				f, ok := fv.r.node.(*Field)
				if !ok {
					return mc.fail(ex.Line, "Variant fields must be Field records")
				}
				v.Fields = append(v.Fields, *f)
			}
		}
		return mc.wrapVariant(v), true
	case "Enum", "Struct", "Func":
		if len(args) != 1 || args[0].kind != ckString || !validIdentName(args[0].s) {
			return mc.fail(ex.Line, "%s takes a name string", id.Name)
		}
		switch id.Name {
		case "Enum":
			return mc.wrapEnum(&EnumDecl{Name: args[0].s, Line: ex.Line}), true
		case "Struct":
			return mc.wrapStruct(&StructDecl{Name: args[0].s, Line: ex.Line}), true
		}
		return mc.wrapFunc(&FuncDecl{Name: args[0].s, Body: &Block{}, Line: ex.Line}), true
	case "len":
		if len(args) != 1 {
			return mc.fail(ex.Line, "len takes 1 argument")
		}
		switch args[0].kind {
		case ckList:
			return intVal(big.NewInt(int64(len(args[0].l)))), true
		case ckString:
			return intVal(big.NewInt(int64(len(args[0].s)))), true
		}
		return mc.fail(ex.Line, "len of %s", metaString(args[0]))
	case "str":
		if len(args) != 1 {
			return mc.fail(ex.Line, "str takes 1 argument")
		}
		return strVal(metaString(args[0])), true
	case "embed":
		// read a file relative to the package directory at comptime
		if len(args) != 1 || args[0].kind != ckString {
			return mc.fail(ex.Line, "embed takes a path string")
		}
		v, ok := mc.c.constEmbed(ex.Line, 0, args[0].s)
		if !ok {
			return constVal{}, false
		}
		return v, true
	case "split":
		if len(args) != 2 || args[0].kind != ckString || args[1].kind != ckString {
			return mc.fail(ex.Line, "split takes (string, string)")
		}
		parts := strings.Split(args[0].s, args[1].s)
		elems := make([]constVal, len(parts))
		for i, p := range parts {
			elems[i] = strVal(p)
		}
		return listVal(elems, nil), true
	case "join":
		if len(args) != 2 || args[0].kind != ckList || args[1].kind != ckString {
			return mc.fail(ex.Line, "join takes (list, string)")
		}
		parts := make([]string, len(args[0].l))
		for i, e := range args[0].l {
			parts[i] = metaString(e)
		}
		return strVal(strings.Join(parts, args[1].s)), true
	case "upper", "lower", "trim":
		if len(args) != 1 || args[0].kind != ckString {
			return mc.fail(ex.Line, "%s takes a string", id.Name)
		}
		s := args[0].s
		switch id.Name {
		case "upper":
			s = strings.ToUpper(s)
		case "lower":
			s = strings.ToLower(s)
		case "trim":
			s = strings.Trim(s, " ")
		}
		return strVal(s), true
	case "replace":
		if len(args) != 3 || args[0].kind != ckString || args[1].kind != ckString || args[2].kind != ckString {
			return mc.fail(ex.Line, "replace takes (string, string, string)")
		}
		return strVal(strings.ReplaceAll(args[0].s, args[1].s, args[2].s)), true
	case "contains", "has_prefix", "has_suffix":
		if len(args) != 2 || args[0].kind != ckString || args[1].kind != ckString {
			return mc.fail(ex.Line, "%s takes (string, string)", id.Name)
		}
		switch id.Name {
		case "contains":
			return boolVal(strings.Contains(args[0].s, args[1].s)), true
		case "has_prefix":
			return boolVal(strings.HasPrefix(args[0].s, args[1].s)), true
		}
		return boolVal(strings.HasSuffix(args[0].s, args[1].s)), true
	case "repeat":
		if len(args) != 2 || args[0].kind != ckString || args[1].kind != ckInt {
			return mc.fail(ex.Line, "repeat takes (string, int)")
		}
		if args[1].i.Sign() < 0 {
			return mc.fail(ex.Line, "repeat count must be non-negative")
		}
		if !args[1].i.IsInt64() || args[1].i.Int64() > 10000 {
			return mc.fail(ex.Line, "repeat count %s exceeds the 10000 limit", args[1].i.String())
		}
		return strVal(strings.Repeat(args[0].s, int(args[1].i.Int64()))), true
	}
	// not a builtin: a previously declared function, interpreted now
	if d := mc.findDecl(id.Name); d != nil {
		if fn, ok := d.(*FuncDecl); ok {
			return mc.callFunc(fn, args, ex.Line)
		}
		return mc.fail(ex.Line, "%s is not a function", id.Name)
	}
	return mc.fail(ex.Line, "undefined comptime function: %s", id.Name)
}
