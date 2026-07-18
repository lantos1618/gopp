package main

import (
	"fmt"
	"strings"
)

// emit.go — go++ v2 compiler: Go code generation from the typed AST.
// Consumes the parser's AST plus the checker's type/resolution tables and
// emits Go source. go++ constructs lower as:
//   - enum          -> tagged struct + iota tags + constructor funcs
//   - match subject -> if/else chain on the tag (IIFE in value position)
//   - match channels-> select
//   - chan[T](cap)  -> make(chan T, cap); .send/.recv/.close -> <- / close
//   - var m map[K]V -> make(...) (maps are never nil in go++)
//   - loop { }      -> labeled for; break loop -> break label

type emitter struct {
	c     *checker
	buf   strings.Builder
	tmp   int
	loops []string
}

func emit(f *File, c *checker) string {
	e := &emitter{c: c}
	e.s("package %s\n\n", f.PkgName)
	for _, d := range f.Decls {
		switch dd := d.(type) {
		case *EnumDecl:
			e.emitEnum(dd)
		case *FuncDecl:
			e.emitFunc(dd)
		}
	}
	return e.buf.String()
}

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
			return "time.Duration"
		}
		return tt.name
	case *tEnum:
		if len(tt.args) == 0 {
			return tt.decl.Name
		}
		parts := make([]string, len(tt.args))
		for i, a := range tt.args {
			parts[i] = e.typeGo(a)
		}
		return tt.decl.Name + "[" + strings.Join(parts, ", ") + "]"
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

// ---------- enums ----------

func (e *emitter) tagConst(d *EnumDecl, v *Variant) string {
	return "__gopp_tag_" + d.Name + "_" + v.Name
}

func (e *emitter) paramName(f Field, k int) string {
	if f.Name != "" {
		return f.Name
	}
	return fmt.Sprintf("v%d", k)
}

func (e *emitter) fieldGo(v *Variant, f Field, k int) string {
	return "__gopp_F_" + v.Name + "_" + e.paramName(f, k)
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
	tagT := "__gopp_tag_" + d.Name
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
	e.s("__gopp_tag %s\n", tagT)
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
		e.s("__gopp_z.__gopp_tag = %s\n", e.tagConst(d, v))
		for k, fld := range v.Fields {
			e.s("__gopp_z.%s = %s\n", e.fieldGo(v, fld, k), e.paramName(fld, k))
		}
		e.s("return __gopp_z\n}\n")
	}
	e.s("\n")
}

// ---------- functions ----------

func (e *emitter) emitFunc(fn *FuncDecl) {
	var params []string
	for _, p := range fn.Params {
		params = append(params, p.Name+" "+e.typeExprGo(p.Type))
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
	e.s("func %s(%s)%s {\n", fn.Name, strings.Join(params, ", "), res)
	e.emitStmts(fn.Body.List)
	e.s("}\n\n")
}

// ---------- statements ----------

func (e *emitter) emitStmts(list []Stmt) {
	for _, s := range list {
		e.emitStmt(s)
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
		switch {
		case st.Init != nil:
			e.s("var %s %s = %s\n", st.Name, ty, e.expr(st.Init))
		case st.Type.(*MapType) != nil:
			// go++ maps are instantiated on declaration — no nil maps
			e.s("var %s %s = make(%s)\n", st.Name, ty, ty)
		default:
			e.s("var %s %s\n", st.Name, ty)
		}
	case *AssignStmt:
		lhs := make([]string, len(st.Lhs))
		for i, l := range st.Lhs {
			lhs[i] = e.expr(l)
		}
		rhs := make([]string, len(st.Rhs))
		for i, r := range st.Rhs {
			rhs[i] = e.expr(r)
		}
		e.s("%s %s %s\n", strings.Join(lhs, ", "), st.Op, strings.Join(rhs, ", "))
	case *ExprStmt:
		if m, ok := st.X.(*MatchExpr); ok {
			e.emitMatch(m, false)
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
		e.s("%s: for {\n", label)
		e.emitStmts(st.Body.List)
		e.s("}\n")
		e.loops = e.loops[:len(e.loops)-1]
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
			// unit variant value: Active -> Status_Active()
			return e.ctorRef(ct, nil) + "()"
		}
		return ex.Name
	case *BinaryExpr:
		return "(" + e.expr(ex.X) + " " + ex.Op + " " + e.expr(ex.Y) + ")"
	case *UnaryExpr:
		return ex.Op + e.expr(ex.X)
	case *CallExpr:
		return e.call(ex)
	case *SelectorExpr:
		return e.expr(ex.X) + "." + ex.Sel
	case *IndexExpr:
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
	}
	return "/* unhandled expr */"
}

// ctorRef names a variant constructor: user enums are prefixed
// (Status_Failed), prelude constructors stay bare (Ok, Err, Some, None).
func (e *emitter) ctorRef(ct *ctorTarget, typeArgs []string) string {
	name := ct.enum.Name + "_" + ct.variant.Name
	if e.c.prelude[ct.enum] {
		name = ct.variant.Name
	}
	if len(typeArgs) > 0 {
		return name + "[" + strings.Join(typeArgs, ", ") + "]"
	}
	return name
}

func (e *emitter) call(ex *CallExpr) string {
	var args []string
	for _, a := range ex.Args {
		args = append(args, e.expr(a))
	}
	argStr := strings.Join(args, ", ")
	switch fun := ex.Fun.(type) {
	case *SelectorExpr:
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
			return e.ctorRef(ct, nil) + "(" + argStr + ")"
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
			e.s("return %s\n", e.expr(a.BodyExpr))
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
			}
		case *SendPat:
			e.s("case %s <- %s:\n", e.expr(p.Chan), e.expr(p.Value))
		case *AfterPat:
			e.s("case <-goppAfter(%s):\n", e.expr(p.D))
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
				renameArm(a, bd, uniq)
			}
		case *IdentPat:
			if e.c.patVariant[p] {
				continue
			}
			uniq := e.tmpName("b")
			e.s("%s := %s\n", uniq, mv)
			renameArm(a, p.Name, uniq)
		}
	}
	for i := range m.Arms {
		a := &m.Arms[i]
		e.chainKw(i, len(m.Arms), a, func() string {
			var cond string
			switch p := a.Pat.(type) {
			case *VariantPat:
				cond = mv + ".__gopp_tag == " + e.tagConst(en.decl, findVariant(en.decl, p.Name))
			case *IdentPat:
				if e.c.patVariant[p] {
					cond = mv + ".__gopp_tag == " + e.tagConst(en.decl, findVariant(en.decl, p.Name))
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
					}
				}
			case *IdentPat:
				if !e.c.patVariant[p] && p.Name != "_" {
					e.s("%s := %s\n", p.Name, mv)
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
	case *ReturnStmt:
		for _, r := range st.Results {
			renameExpr(r, from, to)
		}
	case *IncDecStmt:
		renameExpr(st.X, from, to)
	}
}
