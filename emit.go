package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// emit.go — go++ v2 compiler: Go code generation from the typed AST.
// Consumes the parser's AST plus the checker's type/resolution tables and
// emits Go source. go++ constructs lower as:
//   - enum          -> tagged struct + iota tags + constructor funcs
//   - match subject -> if/else chain on the tag (IIFE in value position)
//   - match channels-> select
//   - chan[T](cap)  -> make(chan T, cap); .send/.recv/.close -> <- / close
//   - var m map<K, V> -> make(...) (maps are never nil in go++)
//   - loop { }      -> labeled for; break loop -> break label

type emitter struct {
	c           *checker
	buf         strings.Builder
	tmp         int
	loops       []string
	curResults  []Type          // enclosing function's results (for ? desugars)
	needTime    bool            // emitted time.Duration somewhere: add the import
	needGopp    bool            // emitted a gopp.* reference: add the prelude import
	usedImports map[string]bool // package qualifiers referenced: add imports
	testMode    bool            // gopp test: user main() is renamed aside
}

func emit(f *File, c *checker) string {
	return emitMode(f, c, false)
}

func emitMode(f *File, c *checker, testMode bool) string {
	e := &emitter{c: c, usedImports: map[string]bool{}, testMode: testMode}
	for _, d := range f.Decls {
		switch dd := d.(type) {
		case *EnumDecl:
			e.emitEnum(dd)
		case *StructDecl:
			e.emitStruct(dd)
		case *BehaviorDecl:
			e.emitBehavior(dd)
		case *ImplDecl:
			e.emitImpl(dd)
		case *FuncDecl:
			if !dd.Native { // native funcs come from the package's .go file
				e.emitFunc(dd)
			}
		}
	}
	// §14: prelude operator interfaces actually referenced by an impl or
	// a bound (unreferenced ones stay unwritten, like the prelude enums)
	var opbs []string
	for b := range e.c.usedPreludeBehavior {
		opbs = append(opbs, b)
	}
	sort.Strings(opbs)
	for _, b := range opbs {
		e.emitOperatorInterface(b)
	}
	head := "package " + f.PkgName + "\n\n"
	if e.needTime {
		head += "import \"time\"\n\n"
	}
	if e.needGopp {
		head += "import \"goppout/gopp\"\n\n"
	}
	// §3: one import per referenced dependency, deterministic order
	var quals []string
	for q := range e.usedImports {
		quals = append(quals, q)
	}
	sort.Strings(quals)
	for _, q := range quals {
		head += "import \"goppout/" + c.importPaths[q] + "\"\n\n"
	}
	return head + e.buf.String()
}

// useImport records a reference to an imported package's qualifier.
func (e *emitter) useImport(qual string) { e.usedImports[qual] = true }

func (e *emitter) s(format string, args ...any) {
	fmt.Fprintf(&e.buf, format, args...)
}

func (e *emitter) tmpName(prefix string) string {
	n := fmt.Sprintf("__gopp_%s%d", prefix, e.tmp)
	e.tmp++
	return n
}

// ---------- type rendering ----------

func (e *emitter) typeGo(t Type) string {
	switch tt := t.(type) {
	case tBasic:
		if tt.name == "duration" {
			e.needTime = true
			return "time.Duration"
		}
		return tt.name
	case *tEnum:
		name := tt.decl.Name
		if e.c.prelude[tt.decl] {
			e.needGopp = true
			name = "gopp." + name
		} else if q := e.c.declPkg[tt.decl]; q != "" { // imported enum (§3)
			e.useImport(q)
			name = q + "." + name
		}
		if len(tt.args) == 0 {
			return name
		}
		parts := make([]string, len(tt.args))
		for i, a := range tt.args {
			parts[i] = e.typeGo(a)
		}
		return name + "[" + strings.Join(parts, ", ") + "]"
	case *tStruct:
		name := tt.decl.Name
		if q := e.c.declPkg[tt.decl]; q != "" { // imported struct (§3)
			e.useImport(q)
			name = q + "." + name
		}
		if len(tt.args) == 0 {
			return name
		}
		parts := make([]string, len(tt.args))
		for i, a := range tt.args {
			parts[i] = e.typeGo(a)
		}
		return name + "[" + strings.Join(parts, ", ") + "]"
	case *tMap:
		return "map[" + e.typeGo(tt.k) + "]" + e.typeGo(tt.v)
	case *tChan:
		return "chan " + e.typeGo(tt.elem)
	case *tSlice:
		return "[]" + e.typeGo(tt.elem)
	case *tStar:
		return "*" + e.typeGo(tt.x)
	case tTypeParam:
		return tt.name
	case *tFunc:
		parts := make([]string, len(tt.params))
		for i, p := range tt.params {
			parts[i] = e.typeGo(p)
		}
		r := ""
		if len(tt.results) > 0 {
			r = " " + e.typeGo(tt.results[0])
		}
		return "func(" + strings.Join(parts, ", ") + ")" + r
	}
	return "any"
}

func (e *emitter) typeExprGo(te TypeExpr) string {
	switch t := te.(type) {
	case *IdentType:
		// prelude type names (Result, Option) are qualified: user code
		// lands in its own package, the prelude in gopp
		if en, ok := e.c.enums[t.Name]; ok && e.c.prelude[en] {
			e.needGopp = true
			return "gopp." + t.Name
		}
		if dot := strings.IndexByte(t.Name, '.'); dot > 0 { // pkg.Type (§3)
			e.useImport(t.Name[:dot])
		}
		return t.Name
	case *IndexType:
		parts := make([]string, len(t.Args))
		for i, a := range t.Args {
			parts[i] = e.typeExprGo(a)
		}
		return e.typeExprGo(t.X) + "[" + strings.Join(parts, ", ") + "]"
	case *MapType:
		return "map[" + e.typeExprGo(t.K) + "]" + e.typeExprGo(t.V)
	case *ChanType:
		return "chan " + e.typeExprGo(t.Elem)
	case *SliceType:
		return "[]" + e.typeExprGo(t.Elem)
	case *StarType:
		return "*" + e.typeExprGo(t.X)
	}
	return "any"
}

// ---------- structs ----------

func (e *emitter) emitStruct(d *StructDecl) {
	tp := ""
	if len(d.TypeParams) > 0 { // §8: type Pair[T any] struct
		tp = "[" + strings.Join(d.TypeParams, " any, ") + " any]"
	}
	e.s("type %s%s struct {\n", d.Name, tp)
	for _, f := range d.Fields {
		e.s("%s %s\n", f.Name, e.typeExprGo(f.Type))
	}
	e.s("}\n\n")
}

// ---------- enums ----------

func (e *emitter) tagConst(d *EnumDecl, v *Variant) string {
	return "Gopp_Tag_" + d.Name + "_" + v.Name
}

// tagRef qualifies a tag constant: prelude enum tags live in the gopp
// package, imported enum tags in their own (§3), local ones stay bare.
func (e *emitter) tagRef(d *EnumDecl, v *Variant) string {
	if e.c.prelude[d] {
		e.needGopp = true
		return "gopp." + e.tagConst(d, v)
	}
	if q := e.c.declPkg[d]; q != "" {
		e.useImport(q)
		return q + "." + e.tagConst(d, v)
	}
	return e.tagConst(d, v)
}

func (e *emitter) paramName(f Field, k int) string {
	if f.Name != "" {
		return f.Name
	}
	return fmt.Sprintf("v%d", k)
}

func (e *emitter) fieldGo(v *Variant, f Field, k int) string {
	return "Gopp_F_" + v.Name + "_" + e.paramName(f, k)
}

func (e *emitter) typeParams(d *EnumDecl) (decl, use string) {
	if len(d.TypeParams) == 0 {
		return "", ""
	}
	decl = "[" + strings.Join(d.TypeParams, " any, ") + " any]"
	use = "[" + strings.Join(d.TypeParams, ", ") + "]"
	return decl, use
}

func (e *emitter) emitEnum(d *EnumDecl) {
	tagT := "gopp_tag_" + d.Name
	e.s("type %s int\n", tagT)
	e.s("const (\n")
	for i, v := range d.Variants {
		if i == 0 {
			e.s("%s %s = iota\n", e.tagConst(d, &v), tagT)
		} else {
			e.s("%s\n", e.tagConst(d, &v))
		}
	}
	e.s(")\n")
	tp, tpu := e.typeParams(d)
	e.s("type %s%s struct {\n", d.Name, tp)
	e.s("Gopp_Tag %s\n", tagT)
	for i := range d.Variants {
		v := &d.Variants[i]
		for k, fld := range v.Fields {
			e.s("%s %s\n", e.fieldGo(v, fld, k), e.typeExprGo(fld.Type))
		}
	}
	e.s("}\n")
	for i := range d.Variants {
		v := &d.Variants[i]
		var params []string
		for k, fld := range v.Fields {
			params = append(params, e.paramName(fld, k)+" "+e.typeExprGo(fld.Type))
		}
		e.s("func %s_%s%s(%s) %s%s {\n", d.Name, v.Name, tp, strings.Join(params, ", "), d.Name, tpu)
		e.s("var __gopp_z %s%s\n", d.Name, tpu)
		e.s("__gopp_z.Gopp_Tag = %s\n", e.tagConst(d, v))
		for k, fld := range v.Fields {
			e.s("__gopp_z.%s = %s\n", e.fieldGo(v, fld, k), e.paramName(fld, k))
		}
		e.s("return __gopp_z\n}\n")
	}
	e.s("\n")
}

// emitTry desugars `x := f()?` (spec §7) at statement level:
//
//	__gopp_tryN := f()
//	if __gopp_tryN.IsErr() { return gopp.Err[Rt, Re](__gopp_tryN.Gopp_F_Err_v0) }
//	<bind>(__gopp_tryN.Gopp_F_Ok_v0)
//
// No wrapping block: the binding must land in the current scope. Sema
// (checkTry) has already proven the function returns a matching Result.
func (e *emitter) emitTry(te *TryExpr, bind func(tmp string)) {
	tmp := e.tmpName("try")
	e.s("%s := %s\n", tmp, e.expr(te.X))
	rt := e.curResults[0].(*tEnum) // checkTry guaranteed Result[T, E]
	e.s("if %s.IsErr() {\n", tmp)
	e.needGopp = true
	e.s("return gopp.Err[%s, %s](%s.Gopp_F_Err_v0)\n", e.typeGo(rt.args[0]), e.typeGo(rt.args[1]), tmp)
	e.s("}\n")
	bind(tmp)
}

// ---------- behaviors (§8) ----------

// emitBehavior lowers a behavior to a Go interface (receiver dropped).
func (e *emitter) emitBehavior(d *BehaviorDecl) {
	e.s("type %s interface {\n", d.Name)
	for _, m := range d.Methods {
		e.s("%s%s\n", m.Name, e.sigGo(m.Params[1:], m.Results))
	}
	e.s("}\n\n")
}

// emitOperatorInterface writes a prelude operator behavior as a Go
// interface generic over Self (§14): Add[T] { add(T) T } and friends.
func (e *emitter) emitOperatorInterface(name string) {
	method, sig := "", ""
	switch name {
	case "Add":
		method, sig = "add", "(T) T"
	case "Sub":
		method, sig = "sub", "(T) T"
	case "Mul":
		method, sig = "mul", "(T) T"
	case "Div":
		method, sig = "div", "(T) T"
	case "Mod":
		method, sig = "mod", "(T) T"
	case "Eq":
		method, sig = "eq", "(T) bool"
	case "Ord":
		method, sig = "cmp", "(T) int"
	case "Neg":
		method, sig = "neg", "() T"
	case "Not":
		method, sig = "not", "() bool"
	default:
		return
	}
	e.s("type %s[T any] interface {\n%s%s\n}\n\n", name, method, sig)
}

// emitImpl lowers impl methods to Go receiver methods — exactly how Go
// interfaces get satisfied. Generic targets render the receiver with
// the type parameters (func (self Box[T]) Show() ...).
func (e *emitter) emitImpl(d *ImplDecl) {
	tn := implTypeName(d.Type) // registerImpls guaranteed this
	rt := e.typeExprGo(d.Type)
	implHas := map[string]bool{}
	for _, m := range d.Methods {
		implHas[m.Name] = true
		ft := e.c.methods[tn][m.Name]
		if ft == nil {
			continue
		}
		e.curResults = ft.results
		recv := recvName(m)
		e.s("func (%s %s) %s%s {\n", recv, rt, m.Name, e.sigGo(m.Params[1:], m.Results))
		e.emitStmts(m.Body.List)
		e.s("}\n\n")
	}
	// default bodies fill the methods the impl didn't provide (§23-lite)
	b := e.c.behaviors[d.Behavior]
	if b == nil {
		return
	}
	for _, bm := range b.Methods {
		if bm.Body == nil || implHas[bm.Name] {
			continue
		}
		ft := e.c.methods[tn][bm.Name]
		if ft == nil {
			continue
		}
		e.curResults = ft.results
		recv := recvNameFields(bm.Params)
		e.s("func (%s %s) %s%s {\n", recv, rt, bm.Name, e.sigGo(bm.Params[1:], bm.Results))
		e.emitStmts(bm.Body.List)
		e.s("}\n\n")
	}
}

// funcName renames the user's main aside in test mode so the generated
// test runner can own main().
func (e *emitter) funcName(fn *FuncDecl) string {
	if e.testMode && fn.Name == "main" {
		return "goppDisabledMain"
	}
	return fn.Name
}

// sigGo renders (params) results without the function name.
func (e *emitter) sigGo(params, results []Field) string {
	var ps []string
	for _, p := range params {
		ps = append(ps, p.Name+" "+e.typeExprGo(p.Type))
	}
	res := ""
	switch len(results) {
	case 0:
	case 1:
		res = " " + e.typeExprGo(results[0].Type)
	default:
		var rs []string
		for _, r := range results {
			rs = append(rs, e.typeExprGo(r.Type))
		}
		res = " (" + strings.Join(rs, ", ") + ")"
	}
	return "(" + strings.Join(ps, ", ") + ")" + res
}

// ---------- functions ----------

func (e *emitter) emitFunc(fn *FuncDecl) {
	e.curResults = e.c.funcs[fn.Name].results
	var params []string
	for _, p := range fn.Params {
		params = append(params, p.Name+" "+e.typeExprGo(p.Type))
	}
	tp := ""
	if len(fn.TypeParams) > 0 { // §8: Go generics carry the instantiation
		parts := make([]string, len(fn.TypeParams))
		for i, tpn := range fn.TypeParams {
			constraint := "any"
			if i < len(fn.Bounds) && fn.Bounds[i] != "" {
				constraint = fn.Bounds[i]
				if e.c.preludeBehavior[constraint] {
					constraint += "[" + tpn + "]" // §14: Add[T], Eq[T], ...
				}
			}
			parts[i] = tpn + " " + constraint
		}
		tp = "[" + strings.Join(parts, ", ") + "]"
	}
	res := ""
	switch len(fn.Results) {
	case 0:
	case 1:
		res = " " + e.typeExprGo(fn.Results[0].Type)
	default:
		var rs []string
		for _, r := range fn.Results {
			rs = append(rs, e.typeExprGo(r.Type))
		}
		res = " (" + strings.Join(rs, ", ") + ")"
	}
	e.s("func %s%s(%s)%s {\n", e.funcName(fn), tp, strings.Join(params, ", "), res)
	e.emitStmts(fn.Body.List)
	e.s("}\n\n")
}

// ---------- statements ----------

func (e *emitter) emitStmts(list []Stmt) {
	for _, s := range list {
		e.emitStmt(s)
		// drop dead code after a diverging statement: sema already warned
		// about it (§9), and Go would reject a function that doesn't END
		// in a terminating statement even when a return appears earlier.
		if e.c.stmtDiverges(s) {
			return
		}
	}
}

func (e *emitter) emitStmt(s Stmt) {
	switch st := s.(type) {
	case *Block:
		e.s("{\n")
		e.emitStmts(st.List)
		e.s("}\n")
	case *VarStmt:
		ty := e.typeExprGo(st.Type)
		if st.Init != nil {
			if te, ok := st.Init.(*TryExpr); ok {
				e.emitTry(te, func(tmp string) {
					e.s("var %s %s = %s.Gopp_F_Ok_v0\n", st.Name, ty, tmp)
				})
			} else {
				e.s("var %s %s = %s\n", st.Name, ty, e.expr(st.Init))
			}
		} else if _, isMap := st.Type.(*MapType); isMap {
			// go++ maps are instantiated on declaration — no nil maps
			e.s("var %s %s = make(%s)\n", st.Name, ty, ty)
		} else {
			e.s("var %s %s\n", st.Name, ty)
		}
	case *AssignStmt:
		if len(st.Lhs) == 1 && len(st.Rhs) == 1 {
			if te, ok := st.Rhs[0].(*TryExpr); ok {
				e.emitTry(te, func(tmp string) {
					e.s("%s %s %s.Gopp_F_Ok_v0\n", e.expr(st.Lhs[0]), st.Op, tmp)
				})
				return
			}
		}
		lhs := make([]string, len(st.Lhs))
		for i, l := range st.Lhs {
			lhs[i] = e.expr(l)
		}
		rhs := make([]string, len(st.Rhs))
		for i, r := range st.Rhs {
			rhs[i] = e.expr(r)
		}
		if len(st.Lhs) == 1 {
			if mn, ok := e.c.operatorOps[st.Lhs[0]]; ok {
				ix, isIndex := st.Lhs[0].(*IndexExpr)
				if isIndex && mn == "set" {
					// §14 overloaded index write: g[i] = v -> g.set(i, v)
					var args []string
					for _, i := range ix.Index {
						args = append(args, e.expr(i))
					}
					args = append(args, e.expr(st.Rhs[0]))
					e.s("%s.set(%s)\n", e.expr(ix.X), strings.Join(args, ", "))
					return
				}
				// §14 compound assignment: x += y -> x = x.add(y)
				e.s("%s = %s.%s(%s)\n", lhs[0], lhs[0], mn, rhs[0])
				return
			}
		}
		e.s("%s %s %s\n", strings.Join(lhs, ", "), st.Op, strings.Join(rhs, ", "))
	case *ExprStmt:
		if m, ok := st.X.(*MatchExpr); ok {
			e.emitMatch(m, false)
			return
		}
		if te, ok := st.X.(*TryExpr); ok {
			e.emitTry(te, func(tmp string) {})
			return
		}
		e.s("%s\n", e.expr(st.X))
	case *IfStmt:
		e.s("if %s {\n", e.expr(st.Cond))
		e.emitStmts(st.Then.List)
		switch els := st.Else.(type) {
		case nil:
			e.s("}\n")
		case *IfStmt:
			e.s("} else ")
			e.emitStmt(els)
		case *Block:
			e.s("} else {\n")
			e.emitStmts(els.List)
			e.s("}\n")
		}
	case *ForStmt:
		if st.Init == nil && st.Cond == nil && st.Post == nil {
			e.s("for {\n")
		} else if st.Init == nil && st.Post == nil {
			e.s("for %s {\n", e.expr(st.Cond))
		} else {
			init, cond, post := "", "", ""
			if st.Init != nil {
				init = e.simpleStmt(st.Init)
			}
			if st.Cond != nil {
				cond = " " + e.expr(st.Cond)
			}
			if st.Post != nil {
				post = " " + e.simpleStmt(st.Post)
			}
			e.s("for %s;%s;%s {\n", init, cond, post)
		}
		e.emitStmts(st.Body.List)
		e.s("}\n")
	case *LoopStmt:
		label := e.tmpName("loop")
		e.loops = append(e.loops, label)
		if bodyHasBreakLoop(st.Body.List) {
			e.s("%s: for {\n", label) // label only when a `break loop` uses it
		} else {
			e.s("for {\n")
		}
		e.emitStmts(st.Body.List)
		e.s("}\n")
		e.loops = e.loops[:len(e.loops)-1]
	case *ForInStmt:
		x := e.expr(st.X)
		switch {
		case st.Var2 != "":
			e.s("for %s, %s := range %s {\n", st.Var, st.Var2, x)
		case isChanType(e.c.types[st.X]):
			e.s("for %s := range %s {\n", st.Var, x)
		default:
			e.s("for _, %s := range %s {\n", st.Var, x)
		}
		e.emitStmts(st.Body.List)
		e.s("}\n")
	case *BreakStmt:
		if st.Label == "loop" {
			e.s("break %s\n", e.loops[len(e.loops)-1])
		} else {
			e.s("break\n")
		}
	case *ReturnStmt:
		if len(st.Results) == 0 {
			e.s("return\n")
			return
		}
		rs := make([]string, len(st.Results))
		for i, r := range st.Results {
			rs[i] = e.expr(r)
		}
		e.s("return %s\n", strings.Join(rs, ", "))
	case *IncDecStmt:
		e.s("%s%s\n", e.expr(st.X), st.Op)
	}
}

// simpleStmt renders init/post statements of a for header.
func (e *emitter) simpleStmt(s Stmt) string {
	return e.capture(func() { e.emitStmt(s) })
}

// capture runs f with output redirected to a fresh buffer and returns it.
func (e *emitter) capture(f func()) string {
	save := e.buf
	e.buf = strings.Builder{}
	f()
	out := e.buf.String()
	e.buf = save
	return strings.TrimSpace(out)
}

// ---------- expressions ----------

func (e *emitter) expr(x Expr) string {
	switch ex := x.(type) {
	case *BasicLit:
		return ex.Value
	case *Ident:
		if ct, ok := e.c.resolved[ex]; ok {
			// unit variant value: Active -> Status_Active(), None -> None[int]()
			return e.ctorRef(ct, e.typeArgsGo(e.c.inferred[ex])) + "()"
		}
		if e.c.preludeVars[ex] {
			// ms/second/minute live in the gopp package, exported
			e.needGopp = true
			return "gopp." + strings.ToUpper(ex.Name[:1]) + ex.Name[1:]
		}
		return ex.Name
	case *BinaryExpr:
		// §14: an operator impl desugars to the method call
		if mn, ok := e.c.operatorOps[ex]; ok {
			x, y := e.expr(ex.X), e.expr(ex.Y)
			switch ex.Op {
			case "!=":
				return "!(" + x + "." + mn + "(" + y + "))"
			case "<", "<=", ">", ">=":
				return "(" + x + "." + mn + "(" + y + ") " + ex.Op + " 0)"
			default: // + - * / % ==
				return "(" + x + "." + mn + "(" + y + "))"
			}
		}
		return "(" + e.expr(ex.X) + " " + ex.Op + " " + e.expr(ex.Y) + ")"
	case *UnaryExpr:
		if mn, ok := e.c.operatorOps[ex]; ok { // §14
			return "(" + e.expr(ex.X) + "." + mn + "())"
		}
		return ex.Op + e.expr(ex.X)
	case *CallExpr:
		return e.call(ex)
	case *ComptimeExpr:
		// sema evaluated it (§10); emit the constant, wrapped in its
		// type when the type isn't the value's default
		cv, ok := e.c.constVals[ex]
		if !ok {
			return "0 /* unreachable: comptime not evaluated */"
		}
		return e.constGo(ex, cv)
	case *SelectorExpr:
		if ct, ok := e.c.resolved[ex]; ok {
			// qualified unit variant value: foo.Active -> foo.Status_Active()
			return e.ctorRef(ct, e.typeArgsGo(e.c.inferred[ex])) + "()"
		}
		if q, ok := e.c.qualified[ex]; ok {
			// qualified function reference: foo.Bar (§3)
			e.useImport(q)
			return q + "." + ex.Sel
		}
		// (*p).Field — a bare unary operand would bind the selector first
		if _, isUnary := ex.X.(*UnaryExpr); isUnary {
			return "(" + e.expr(ex.X) + ")." + ex.Sel
		}
		return e.expr(ex.X) + "." + ex.Sel
	case *IndexExpr:
		if mn, ok := e.c.operatorOps[ex]; ok {
			// §14 overloaded index read: g[i, j] -> g.index(i, j)
			var idx []string
			for _, i := range ex.Index {
				idx = append(idx, e.expr(i))
			}
			return e.expr(ex.X) + "." + mn + "(" + strings.Join(idx, ", ") + ")"
		}
		if id, ok := ex.X.(*Ident); ok {
			if ct, ok := e.c.resolved[id]; ok {
				// generic constructor instantiation: Ok[int, string]
				var args []string
				for _, a := range ex.Index {
					args = append(args, e.expr(a))
				}
				return e.ctorRef(ct, args)
			}
		}
		if sel, ok := ex.X.(*SelectorExpr); ok {
			if ct, ok := e.c.resolved[sel]; ok {
				// qualified generic constructor: foo.Box[int]
				var args []string
				for _, a := range ex.Index {
					args = append(args, e.expr(a))
				}
				return e.ctorRef(ct, args)
			}
		}
		var idx []string
		for _, i := range ex.Index {
			idx = append(idx, e.expr(i))
		}
		return e.expr(ex.X) + "[" + strings.Join(idx, ", ") + "]"
	case *MakeChanExpr:
		et := e.typeExprGo(ex.Elem)
		if ex.Cap != nil {
			return fmt.Sprintf("make(chan %s, %s)", et, e.expr(ex.Cap))
		}
		return fmt.Sprintf("make(chan %s)", et)
	case *MatchExpr:
		return e.capture(func() { e.emitMatch(ex, true) })
	case *StructLitExpr:
		parts := make([]string, len(ex.Fields))
		for i, fv := range ex.Fields {
			if fv.Name != "" {
				parts[i] = fv.Name + ": " + e.expr(fv.Value)
			} else {
				parts[i] = e.expr(fv.Value)
			}
		}
		return e.typeExprGo(ex.Type) + "{" + strings.Join(parts, ", ") + "}"
	case *StringInterpExpr:
		parts := make([]string, len(ex.Parts))
		for i, pt := range ex.Parts {
			parts[i] = e.expr(pt)
		}
		e.needGopp = true
		return "gopp.Str(" + strings.Join(parts, ", ") + ")"
	case *MapLitExpr:
		parts := make([]string, len(ex.Entries))
		for i, en := range ex.Entries {
			parts[i] = e.expr(en.Key) + ": " + e.expr(en.Value)
		}
		return "map[" + e.typeExprGo(ex.K) + "]" + e.typeExprGo(ex.V) + "{" + strings.Join(parts, ", ") + "}"
	case *SliceLitExpr:
		parts := make([]string, len(ex.Values))
		for i, v := range ex.Values {
			parts[i] = e.expr(v)
		}
		return "[]" + e.typeExprGo(ex.Elem) + "{" + strings.Join(parts, ", ") + "}"
	}
	return "/* unhandled expr */"
}

// isChanType reports whether t is a channel type.
func isChanType(t Type) bool {
	_, ok := t.(*tChan)
	return ok
}

// ctorRef names a variant constructor: local enums are prefixed
// (Status_Failed), imported ones qualified (foo.Status_Failed, §3),
// prelude constructors go through the gopp package (gopp.Ok, gopp.Some).
func (e *emitter) ctorRef(ct *ctorTarget, typeArgs []string) string {
	name := ct.enum.Name + "_" + ct.variant.Name
	if e.c.prelude[ct.enum] {
		e.needGopp = true
		name = "gopp." + ct.variant.Name
	} else if q := e.c.declPkg[ct.enum]; q != "" {
		e.useImport(q)
		name = q + "." + name
	}
	if len(typeArgs) > 0 {
		return name + "[" + strings.Join(typeArgs, ", ") + "]"
	}
	return name
}

// typeArgsGo renders inferred type arguments for a constructor reference
// (nil for explicit or non-generic ctors — the reference stays bare).
func (e *emitter) typeArgsGo(ts []Type) []string {
	if len(ts) == 0 {
		return nil
	}
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = e.typeGo(t)
	}
	return out
}

// constGo renders a compile-time-evaluated constant as Go source. A typed
// result keeps its type via a wrapping conversion (x := int8(5), not
// x := 5); untyped-default values stay bare. Negative numbers are
// parenthesized so the literal is safe in any expression context.
func (e *emitter) constGo(ex *ComptimeExpr, cv constVal) string {
	t := e.c.types[ex]
	switch cv.kind {
	case ckInt:
		s := cv.i.String()
		if cv.i.Sign() < 0 {
			s = "(" + s + ")"
		}
		if b, ok := t.(tBasic); ok && b.name != "int" {
			return b.name + "(" + s + ")"
		}
		return s
	case ckDuration:
		e.needTime = true
		return "time.Duration(" + cv.i.String() + ")"
	case ckFloat:
		s := strconv.FormatFloat(cv.f, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eE") {
			s += ".0" // keep it a float literal
		}
		if cv.f < 0 {
			s = "(" + s + ")"
		}
		if b, ok := t.(tBasic); ok && b.name == "float32" {
			return "float32(" + s + ")"
		}
		return s
	case ckString:
		return strconv.Quote(cv.s)
	case ckBool:
		if cv.b {
			return "true"
		}
		return "false"
	case ckRune:
		return strconv.QuoteRune(rune(cv.i.Int64()))
	}
	return "0 /* unreachable: bad const */"
}

func (e *emitter) call(ex *CallExpr) string {
	var args []string
	for _, a := range ex.Args {
		args = append(args, e.expr(a))
	}
	argStr := strings.Join(args, ", ")
	switch fun := ex.Fun.(type) {
	case *SelectorExpr:
		if ct, ok := e.c.resolved[fun]; ok {
			// qualified constructor call: foo.Failed("x") -> foo.Status_Failed("x")
			return e.ctorRef(ct, e.typeArgsGo(e.c.inferred[fun])) + "(" + argStr + ")"
		}
		if q, ok := e.c.qualified[fun]; ok {
			// qualified function call: foo.Bar(1) (§3)
			e.useImport(q)
			return q + "." + fun.Sel + "(" + argStr + ")"
		}
		if _, isChan := e.c.types[fun.X].(*tChan); isChan {
			switch fun.Sel {
			case "send":
				return e.expr(fun.X) + " <- " + argStr
			case "recv":
				return "<-" + e.expr(fun.X)
			case "close":
				return "close(" + e.expr(fun.X) + ")"
			}
		}
		return e.expr(fun.X) + "." + fun.Sel + "(" + argStr + ")"
	case *Ident:
		if ct, ok := e.c.resolved[fun]; ok {
			// inferred type args ride along: Ok(1) -> Ok[int, string](1)
			return e.ctorRef(ct, e.typeArgsGo(e.c.inferred[fun])) + "(" + argStr + ")"
		}
		if fun.Name == "println" || fun.Name == "print" {
			// go++'s own helpers: stdout + %v, not Go's builtin println
			e.needGopp = true
			return "gopp.P" + fun.Name[1:] + "(" + argStr + ")"
		}
		if fun.Name == "assert" {
			e.needGopp = true
			return "gopp.Assert(" + argStr + ")"
		}
		if fun.Name == "assertEq" {
			e.needGopp = true
			return "gopp.AssertEq(" + argStr + ")"
		}
		return fun.Name + "(" + argStr + ")"
	default:
		return e.expr(ex.Fun) + "(" + argStr + ")"
	}
}

// ---------- match ----------

func (e *emitter) emitMatch(m *MatchExpr, valueCtx bool) {
	if valueCtx {
		e.s("func() %s { ", e.typeGo(e.c.types[m]))
	}
	hasChan := false
	for _, a := range m.Arms {
		switch a.Pat.(type) {
		case *RecvPat, *SendPat, *AfterPat, *ClosedPat:
			hasChan = true
		}
	}
	switch {
	case hasChan:
		e.emitSelect(m, valueCtx)
	case m.Subject == nil:
		e.emitBoolChain(m, valueCtx)
	default:
		e.emitSubjectChain(m, valueCtx)
	}
	if valueCtx {
		e.s("}()")
	}
}

func (e *emitter) armBody(a *MatchArm, valueCtx bool) {
	if a.BodyExpr != nil {
		if mx, ok := a.BodyExpr.(*MatchExpr); ok && !valueCtx {
			e.emitMatch(mx, false)
			return
		}
		if valueCtx {
			if isNever(e.c.types[a.BodyExpr]) {
				// diverging arm (e.g. panic): a statement, not a return —
				// sema's tNever already proved the other arms produce the value
				e.s("%s\n", e.expr(a.BodyExpr))
			} else {
				e.s("return %s\n", e.expr(a.BodyExpr))
			}
		} else {
			e.s("%s\n", e.expr(a.BodyExpr))
		}
		return
	}
	e.s("{\n")
	e.emitStmts(a.Body)
	e.s("}\n")
}

func (e *emitter) emitSelect(m *MatchExpr, valueCtx bool) {
	e.s("select {\n")
	for i := range m.Arms {
		a := &m.Arms[i]
		switch p := a.Pat.(type) {
		case *RecvPat:
			if p.Bind == "" || p.Bind == "_" {
				e.s("case <-%s:\n", e.expr(p.Chan))
			} else {
				e.s("case %s := <-%s:\n", p.Bind, e.expr(p.Chan))
				e.s("_ = %s\n", p.Bind) // unused recv bindings are legal in go++
			}
		case *SendPat:
			e.s("case %s <- %s:\n", e.expr(p.Chan), e.expr(p.Value))
		case *AfterPat:
			e.s("case <-gopp.GoppAfter(%s):\n", e.expr(p.D))
			e.needGopp = true
		case *WildcardPat:
			e.s("default:\n")
		}
		e.armBody(a, valueCtx)
	}
	e.s("}\n")
}

func (e *emitter) emitBoolChain(m *MatchExpr, valueCtx bool) {
	e.s("{ ")
	for i := range m.Arms {
		a := &m.Arms[i]
		e.chainKw(i, len(m.Arms), a, func() string {
			return "(" + e.expr(a.Pat.(*BoolPat).X) + ")"
		}, valueCtx)
		e.armBody(a, valueCtx)
		e.s("}")
	}
	e.trailingPanic(m)
	e.s("\n}\n")
}

func (e *emitter) emitSubjectChain(m *MatchExpr, valueCtx bool) {
	mv := e.tmpName("m")
	en, _ := e.c.types[m.Subject].(*tEnum)
	e.s("{ %s := %s\n", mv, e.expr(m.Subject))
	// hoist bindings used by guards (guards evaluate before the arm block)
	for i := range m.Arms {
		a := &m.Arms[i]
		if a.Guard == nil {
			continue
		}
		switch p := a.Pat.(type) {
		case *VariantPat:
			v := findVariant(en.decl, p.Name)
			for k, bd := range p.Bindings {
				if bd == "_" {
					continue
				}
				uniq := e.tmpName("b")
				e.s("%s := %s.%s\n", uniq, mv, e.fieldGo(v, v.Fields[k], k))
				e.s("_ = %s\n", uniq)
				renameArm(a, bd, uniq)
			}
		case *IdentPat:
			if e.c.patVariant[p] {
				continue
			}
			uniq := e.tmpName("b")
			e.s("%s := %s\n", uniq, mv)
			e.s("_ = %s\n", uniq)
			renameArm(a, p.Name, uniq)
		}
	}
	for i := range m.Arms {
		a := &m.Arms[i]
		e.chainKw(i, len(m.Arms), a, func() string {
			var cond string
			switch p := a.Pat.(type) {
			case *VariantPat:
				cond = mv + ".Gopp_Tag == " + e.tagRef(en.decl, findVariant(en.decl, p.Name))
			case *IdentPat:
				if e.c.patVariant[p] {
					cond = mv + ".Gopp_Tag == " + e.tagRef(en.decl, findVariant(en.decl, p.Name))
				} else {
					cond = "true"
				}
			case *LiteralPat:
				cond = mv + " == (" + e.expr(p.X) + ")"
			}
			if a.Guard != nil {
				g := e.expr(a.Guard)
				if cond == "" {
					return "(" + g + ")"
				}
				return "(" + cond + ") && (" + g + ")"
			}
			return cond
		}, valueCtx)
		// unguarded bindings live inside the arm block
		if a.Guard == nil {
			switch p := a.Pat.(type) {
			case *VariantPat:
				v := findVariant(en.decl, p.Name)
				for k, bd := range p.Bindings {
					if bd != "_" {
						e.s("%s := %s.%s\n", bd, mv, e.fieldGo(v, v.Fields[k], k))
						e.s("_ = %s\n", bd) // Go errors on unused bindings; go++ allows them
					}
				}
			case *IdentPat:
				if !e.c.patVariant[p] && p.Name != "_" {
					e.s("%s := %s\n", p.Name, mv)
					e.s("_ = %s\n", p.Name)
				}
			}
		}
		e.armBody(a, valueCtx)
		e.s("}")
	}
	e.trailingPanic(m)
	e.s("\n}\n")
}

// chainKw writes the if/else-if/else keyword plus opening brace for arm i.
func (e *emitter) chainKw(i, n int, a *MatchArm, cond func() string, valueCtx bool) {
	_, isWild := a.Pat.(*WildcardPat)
	if i > 0 {
		e.s(" ")
	}
	switch {
	case isWild && a.Guard == nil && i == 0:
		e.s("if true {\n")
	case isWild && a.Guard == nil:
		e.s("else {\n")
	case i == 0:
		e.s("if %s {\n", cond())
	default:
		e.s("else if %s {\n", cond())
	}
}

func (e *emitter) trailingPanic(m *MatchExpr) {
	last := m.Arms[len(m.Arms)-1]
	_, wildLast := last.Pat.(*WildcardPat)
	if !wildLast || last.Guard != nil {
		e.s(" else { panic(\"go++: non-exhaustive match\") }")
	}
}

// ---------- identifier renaming (for hoisted guard bindings) ----------

func renameArm(a *MatchArm, from, to string) {
	if a.Guard != nil {
		renameExpr(a.Guard, from, to)
	}
	if a.BodyExpr != nil {
		renameExpr(a.BodyExpr, from, to)
	}
	for _, s := range a.Body {
		renameStmt(s, from, to)
	}
}

func renameExpr(x Expr, from, to string) {
	switch ex := x.(type) {
	case *Ident:
		if ex.Name == from {
			ex.Name = to
		}
	case *BinaryExpr:
		renameExpr(ex.X, from, to)
		renameExpr(ex.Y, from, to)
	case *UnaryExpr:
		renameExpr(ex.X, from, to)
	case *CallExpr:
		renameExpr(ex.Fun, from, to)
		for _, a := range ex.Args {
			renameExpr(a, from, to)
		}
	case *SelectorExpr:
		renameExpr(ex.X, from, to)
	case *IndexExpr:
		renameExpr(ex.X, from, to)
		for _, i := range ex.Index {
			renameExpr(i, from, to)
		}
	case *MakeChanExpr:
		if ex.Cap != nil {
			renameExpr(ex.Cap, from, to)
		}
	case *MatchExpr:
		if ex.Subject != nil {
			renameExpr(ex.Subject, from, to)
		}
		for i := range ex.Arms {
			renameArm(&ex.Arms[i], from, to)
		}
	case *ComptimeExpr:
		renameExpr(ex.X, from, to)
	case *SliceLitExpr:
		for _, v := range ex.Values {
			renameExpr(v, from, to)
		}
	case *StructLitExpr:
		for _, fv := range ex.Fields {
			renameExpr(fv.Value, from, to)
		}
	case *StringInterpExpr:
		for _, pt := range ex.Parts {
			renameExpr(pt, from, to)
		}
	case *MapLitExpr:
		for _, en := range ex.Entries {
			renameExpr(en.Key, from, to)
			renameExpr(en.Value, from, to)
		}
	}
}

func renameStmt(s Stmt, from, to string) {
	switch st := s.(type) {
	case *Block:
		for _, ss := range st.List {
			renameStmt(ss, from, to)
		}
	case *VarStmt:
		if st.Init != nil {
			renameExpr(st.Init, from, to)
		}
	case *AssignStmt:
		for _, l := range st.Lhs {
			renameExpr(l, from, to)
		}
		for _, r := range st.Rhs {
			renameExpr(r, from, to)
		}
	case *ExprStmt:
		renameExpr(st.X, from, to)
	case *IfStmt:
		if st.Cond != nil {
			renameExpr(st.Cond, from, to)
		}
		renameStmt(st.Then, from, to)
		if st.Else != nil {
			renameStmt(st.Else, from, to)
		}
	case *ForStmt:
		if st.Init != nil {
			renameStmt(st.Init, from, to)
		}
		if st.Cond != nil {
			renameExpr(st.Cond, from, to)
		}
		if st.Post != nil {
			renameStmt(st.Post, from, to)
		}
		renameStmt(st.Body, from, to)
	case *LoopStmt:
		renameStmt(st.Body, from, to)
	case *ForInStmt:
		renameExpr(st.X, from, to)
		renameStmt(st.Body, from, to)
	case *ReturnStmt:
		for _, r := range st.Results {
			renameExpr(r, from, to)
		}
	case *IncDecStmt:
		renameExpr(st.X, from, to)
	}
}
