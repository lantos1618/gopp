package main

import (
	"fmt"
	"strings"
)

// sema.go — go++ v2 compiler: semantic analysis.
// Name resolution, a real type system (incl. generic enum instantiation),
// and compile-time exhaustiveness checking for match. Errors panic with
// semaError and are recovered in check().

type semaError struct{ msg string }

func (e semaError) Error() string { return e.msg }

func serr(line int, format string, args ...any) {
	panic(semaError{fmt.Sprintf("line %d: %s", line, fmt.Sprintf(format, args...))})
}

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

var (
	tint      = tBasic{"int"}
	tstring   = tBasic{"string"}
	tbool     = tBasic{"bool"}
	tfloat64  = tBasic{"float64"}
	trune     = tBasic{"rune"}
	tduration = tBasic{"duration"}
	tvoid     = tVoid{}
	tany      = tBasic{"any"}
)

var basicTypes = map[string]bool{
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true, "byte": true, "rune": true,
	"string": true, "bool": true, "float32": true, "float64": true,
	"error": true, "any": true, "complex64": true, "complex128": true,
}

func sameType(a, b Type) bool {
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
	enums      map[string]*EnumDecl
	prelude    map[*EnumDecl]bool // synthetic prelude enums (Result, Option)
	funcs      map[string]*tFunc
	ctors      map[string]*ctorTarget
	ambiguous  map[string]bool
	globals    *scope
	cur        *scope
	curResults []Type
	loopDepth  int
	// outputs for the emitter
	types      map[Expr]Type
	resolved   map[Expr]*ctorTarget // idents/call-funs that are variant references
	patVariant map[Pattern]bool     // IdentPat that matches a unit variant (not a binding)
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

func check(f *File) (c *checker, err error) {
	defer func() {
		if r := recover(); r != nil {
			if se, ok := r.(semaError); ok {
				err = se
				return
			}
			panic(r)
		}
	}()
	c = &checker{
		enums:      map[string]*EnumDecl{},
		prelude:    map[*EnumDecl]bool{},
		funcs:      map[string]*tFunc{},
		ctors:      map[string]*ctorTarget{},
		ambiguous:  map[string]bool{},
		globals:    &scope{vars: map[string]Type{}},
		types:      map[Expr]Type{},
		resolved:   map[Expr]*ctorTarget{},
		patVariant: map[Pattern]bool{},
	}
	for _, e := range preludeEnums() {
		c.enums[e.Name] = e
		c.prelude[e] = true
	}
	// prelude duration vars
	c.globals.vars["ms"] = tduration
	c.globals.vars["second"] = tduration
	c.globals.vars["minute"] = tduration
	// register user enums
	for _, d := range f.Decls {
		if e, ok := d.(*EnumDecl); ok {
			if _, dup := c.enums[e.Name]; dup {
				serr(e.Line, "duplicate type %s", e.Name)
			}
			c.enums[e.Name] = e
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
	// function signatures
	for _, d := range f.Decls {
		if fn, ok := d.(*FuncDecl); ok {
			if _, dup := c.funcs[fn.Name]; dup {
				serr(fn.Line, "duplicate function %s", fn.Name)
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
	// function bodies
	for _, d := range f.Decls {
		if fn, ok := d.(*FuncDecl); ok {
			ft := c.funcs[fn.Name]
			c.curResults = ft.results
			c.cur = &scope{parent: c.globals, vars: map[string]Type{}}
			for i, p := range fn.Params {
				c.cur.vars[p.Name] = ft.params[i]
			}
			c.checkStmts(fn.Body.List)
		}
	}
	return c, nil
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
		if basicTypes[t.Name] {
			return tBasic{t.Name}
		}
		if e, ok := c.enums[t.Name]; ok {
			if len(e.TypeParams) > 0 {
				serr(t.Line, "enum %s is generic: use %s[%s]", t.Name, t.Name, strings.Join(e.TypeParams, ", "))
			}
			return &tEnum{decl: e}
		}
		serr(t.Line, "undefined type %s", t.Name)
	case *IndexType:
		base, ok := t.X.(*IdentType)
		if !ok {
			serr(t.Line, "invalid generic type")
		}
		e, ok := c.enums[base.Name]
		if !ok {
			serr(t.Line, "%s is not a generic enum", base.Name)
		}
		if len(e.TypeParams) != len(t.Args) {
			serr(t.Line, "%s takes %d type argument(s), got %d", base.Name, len(e.TypeParams), len(t.Args))
		}
		var args []Type
		for _, a := range t.Args {
			args = append(args, c.resolveTypeIn(a, inEnum))
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
	serr(0, "unknown type expression")
	return nil
}

// exprToType converts a parsed expression back into a type expression,
// for generic instantiations like Ok[int, string].
func exprToType(e Expr) TypeExpr {
	switch ex := e.(type) {
	case *Ident:
		return &IdentType{Name: ex.Name, Line: ex.Line}
	case *IndexExpr:
		base := exprToType(ex.X)
		if base == nil {
			return nil
		}
		it := &IndexType{X: base, Line: ex.Line}
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

func (c *checker) checkStmt(s Stmt) {
	switch st := s.(type) {
	case *Block:
		c.child()
		c.checkStmts(st.List)
		c.pop()
	case *VarStmt:
		ty := c.resolveType(st.Type)
		if st.Init != nil {
			it := c.checkExpr(st.Init)
			c.assignable(it, ty, st.Line)
		}
		c.cur.vars[st.Name] = ty
	case *AssignStmt:
		if len(st.Lhs) != len(st.Rhs) {
			serr(st.Line, "assignment mismatch: %d left, %d right", len(st.Lhs), len(st.Rhs))
		}
		for i := range st.Lhs {
			rt := c.checkExpr(st.Rhs[i])
			if mx, ok := st.Rhs[i].(*MatchExpr); ok && sameType(rt, tvoid) {
				serr(mx.Line, "match in value context must produce a value in every arm")
			}
			if st.Op == ":=" {
				id, ok := st.Lhs[i].(*Ident)
				if !ok {
					serr(st.Line, "left side of := must be a name")
				}
				if id.Name != "_" {
					c.cur.vars[id.Name] = rt
				}
			} else {
				lt := c.checkExpr(st.Lhs[i])
				c.assignable(rt, lt, st.Line)
			}
		}
	case *ExprStmt:
		c.checkExpr(st.X)
	case *IfStmt:
		ct := c.checkExpr(st.Cond)
		if !sameType(ct, tbool) {
			serr(st.Line, "if condition must be bool, got %s", ct)
		}
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
			if !sameType(ct, tbool) {
				serr(st.Line, "for condition must be bool, got %s", ct)
			}
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
				serr(st.Line, "break loop outside of a loop block")
			}
		} else if st.Label != "" {
			serr(st.Line, "unknown label %s", st.Label)
		}
	case *ReturnStmt:
		if len(c.curResults) == 0 {
			if len(st.Results) != 0 {
				serr(st.Line, "function has no results, return has %d", len(st.Results))
			}
			return
		}
		if len(st.Results) != len(c.curResults) {
			serr(st.Line, "return has %d value(s), function declares %d", len(st.Results), len(c.curResults))
		}
		for i, r := range st.Results {
			rt := c.checkExpr(r)
			if mx, ok := r.(*MatchExpr); ok && sameType(rt, tvoid) {
				serr(mx.Line, "match in value context must produce a value in every arm")
			}
			c.assignable(rt, c.curResults[i], st.Line)
		}
	case *IncDecStmt:
		xt := c.checkExpr(st.X)
		if !isNumeric(xt) {
			serr(st.Line, "%s needs a number, got %s", st.Op, xt)
		}
	}
}

func isNumeric(t Type) bool {
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

func (c *checker) assignable(from, to Type, line int) {
	if sameType(from, to) || sameType(to, tany) {
		return
	}
	// untyped constants flowing into numeric types
	if sameType(from, tint) && isNumeric(to) {
		return
	}
	serr(line, "cannot use %s as %s", from, to)
}

// ---------- expressions ----------

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
			ty = tint
		case kFloat:
			ty = tfloat64
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
		switch ex.Op {
		case "==", "!=", "<", "<=", ">", ">=", "&&", "||":
			ty = tbool
		case "+":
			if sameType(xt, tstring) || sameType(yt, tstring) {
				ty = tstring
			} else {
				ty = arithType(xt, yt)
			}
		default:
			ty = arithType(xt, yt)
		}
	case *UnaryExpr:
		xt := c.checkExpr(ex.X)
		switch ex.Op {
		case "<-":
			if ch, ok := xt.(*tChan); ok {
				ty = ch.elem
			} else {
				serr(ex.Line, "cannot receive from non-channel %s", xt)
			}
		case "!":
			ty = tbool
		default:
			ty = xt
		}
	case *CallExpr:
		ty = c.checkCall(ex)
	case *SelectorExpr:
		ty = c.checkSelector(ex)
	case *IndexExpr:
		ty = c.checkIndex(ex)
	case *MakeChanExpr:
		et := c.resolveType(ex.Elem)
		if ex.Cap != nil {
			ct := c.checkExpr(ex.Cap)
			if !isNumeric(ct) {
				serr(ex.Line, "channel capacity must be a number, got %s", ct)
			}
		}
		ty = &tChan{elem: et}
	case *MatchExpr:
		ty = c.checkMatch(ex)
	default:
		serr(0, "unhandled expression %T", e)
	}
	c.types[e] = ty
	return ty
}

func arithType(x, y Type) Type {
	if sameType(x, tduration) || sameType(y, tduration) {
		return tduration
	}
	if sameType(x, tfloat64) || sameType(y, tfloat64) {
		return tfloat64
	}
	return x
}

func (c *checker) checkIdentValue(ex *Ident) Type {
	if t, ok := c.cur.lookup(ex.Name); ok {
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
			serr(ex.Line, "variant name %s is ambiguous (multiple enums)", ex.Name)
		}
		c.resolved[ex] = ct
		et := &tEnum{decl: ct.enum}
		if len(ct.enum.TypeParams) > 0 {
			serr(ex.Line, "%s is generic; use explicit type arguments like %s[..](...)", ex.Name, ex.Name)
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
	serr(ex.Line, "undefined: %s", ex.Name)
	return nil
}

func (c *checker) checkCall(ex *CallExpr) Type {
	switch fun := ex.Fun.(type) {
	case *Ident:
		switch fun.Name {
		case "println", "print", "panic":
			for _, a := range ex.Args {
				c.checkExpr(a)
			}
			return tvoid
		case "len", "cap":
			if len(ex.Args) != 1 {
				serr(ex.Line, "%s takes 1 argument", fun.Name)
			}
			c.checkExpr(ex.Args[0])
			return tint
		case "append":
			if len(ex.Args) < 1 {
				serr(ex.Line, "append needs arguments")
			}
			return c.checkExpr(ex.Args[0])
		}
		if ft, ok := c.funcs[fun.Name]; ok {
			c.checkCallArgs(ex, ft.params)
			if len(ft.results) > 0 {
				return ft.results[0]
			}
			return tvoid
		}
		if ct, ok := c.ctors[fun.Name]; ok {
			return c.callVariantCtor(ex, fun, ct, nil)
		}
		serr(ex.Line, "undefined function: %s", fun.Name)
	case *IndexExpr:
		// generic constructor instantiation: Ok[int, string](v)
		if id, ok := fun.X.(*Ident); ok {
			if ct, ok := c.ctors[id.Name]; ok {
				var args []Type
				for _, te := range fun.Index {
					tt := exprToType(te)
					if tt == nil {
						serr(ex.Line, "invalid type argument")
					}
					args = append(args, c.resolveType(tt))
				}
				return c.callVariantCtor(ex, id, ct, args)
			}
		}
		serr(ex.Line, "not a generic constructor call")
	case *SelectorExpr:
		xt := c.checkExpr(fun.X)
		if ch, ok := xt.(*tChan); ok {
			switch fun.Sel {
			case "send":
				if len(ex.Args) != 1 {
					serr(ex.Line, "send takes 1 argument")
				}
				vt := c.checkExpr(ex.Args[0])
				c.assignable(vt, ch.elem, ex.Line)
				return tvoid
			case "recv":
				if len(ex.Args) != 0 {
					serr(ex.Line, "recv takes no arguments")
				}
				return ch.elem
			case "close":
				if len(ex.Args) != 0 {
					serr(ex.Line, "close takes no arguments")
				}
				return tvoid
			case "closed":
				serr(ex.Line, ".closed() is not supported in v2 (Go channels cannot peek)")
			}
			serr(ex.Line, "channels have no method %s", fun.Sel)
		}
		if en, ok := xt.(*tEnum); ok && en.decl.Name == "Result" {
			if fun.Sel == "IsOk" || fun.Sel == "IsErr" {
				if len(ex.Args) != 0 {
					serr(ex.Line, "%s takes no arguments", fun.Sel)
				}
				return tbool
			}
		}
		serr(ex.Line, "%s has no method %s", xt, fun.Sel)
	}
	serr(ex.Line, "not callable")
	return nil
}

func (c *checker) callVariantCtor(ex *CallExpr, id *Ident, ct *ctorTarget, args []Type) Type {
	if c.ambiguous[id.Name] {
		serr(ex.Line, "variant name %s is ambiguous (multiple enums)", id.Name)
	}
	if len(ct.enum.TypeParams) > 0 {
		if args == nil {
			serr(ex.Line, "%s is generic; use explicit type arguments like %s[..](...)", id.Name, id.Name)
		}
		if len(args) != len(ct.enum.TypeParams) {
			serr(ex.Line, "%s takes %d type argument(s), got %d", id.Name, len(ct.enum.TypeParams), len(args))
		}
	} else if args != nil {
		serr(ex.Line, "%s is not generic", id.Name)
	}
	et := &tEnum{decl: ct.enum, args: args}
	if len(ex.Args) != len(ct.variant.Fields) {
		serr(ex.Line, "%s takes %d value(s), got %d", id.Name, len(ct.variant.Fields), len(ex.Args))
	}
	for i, f := range ct.variant.Fields {
		ft := c.resolveTypeIn(f.Type, ct.enum)
		if args != nil {
			ft = subst(ft, ct.enum.TypeParams, args)
		}
		at := c.checkExpr(ex.Args[i])
		c.assignable(at, ft, ex.Line)
	}
	c.resolved[id] = ct
	return et
}

func (c *checker) checkCallArgs(ex *CallExpr, params []Type) {
	if len(ex.Args) != len(params) {
		serr(ex.Line, "expected %d argument(s), got %d", len(params), len(ex.Args))
	}
	for i := range ex.Args {
		at := c.checkExpr(ex.Args[i])
		c.assignable(at, params[i], ex.Line)
	}
}

func (c *checker) checkSelector(ex *SelectorExpr) Type {
	xt := c.checkExpr(ex.X)
	if en, ok := xt.(*tEnum); ok && en.decl.Name == "Result" {
		if ex.Sel == "IsOk" || ex.Sel == "IsErr" {
			return &tFunc{results: []Type{tbool}}
		}
	}
	serr(ex.Line, "%s has no field or method %s", xt, ex.Sel)
	return nil
}

func (c *checker) checkIndex(ex *IndexExpr) Type {
	// generic instantiation in type position is handled by checkCall;
	// here it's ordinary indexing.
	xt := c.checkExpr(ex.X)
	if len(ex.Index) != 1 {
		serr(ex.Line, "expected 1 index")
	}
	it := c.checkExpr(ex.Index[0])
	switch t := xt.(type) {
	case *tMap:
		c.assignable(it, t.k, ex.Line)
		return t.v
	case *tSlice:
		if !isNumeric(it) {
			serr(ex.Line, "slice index must be a number, got %s", it)
		}
		return t.elem
	}
	serr(ex.Line, "cannot index %s", xt)
	return nil
}

// ---------- match ----------

func (c *checker) checkMatch(ex *MatchExpr) Type {
	hasChan := false
	for _, a := range ex.Arms {
		switch a.Pat.(type) {
		case *RecvPat, *SendPat, *AfterPat, *ClosedPat:
			hasChan = true
		}
	}
	if hasChan {
		return c.checkMatchSelect(ex)
	}
	if ex.Subject == nil {
		return c.checkMatchBool(ex)
	}
	return c.checkMatchSubject(ex)
}

func (c *checker) fieldType(en *tEnum, v *Variant, k int) Type {
	ft := c.resolveTypeIn(v.Fields[k].Type, en.decl)
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

func (c *checker) checkArmBody(a *MatchArm) Type {
	if a.BodyExpr != nil {
		return c.checkExpr(a.BodyExpr)
	}
	c.child()
	c.checkStmts(a.Body)
	c.pop()
	return tvoid
}

func (c *checker) unify(cur, next Type, line int) Type {
	if cur == nil {
		return next
	}
	if !sameType(cur, next) {
		serr(line, "match arms produce different types (%s vs %s)", cur, next)
	}
	return cur
}

func (c *checker) checkMatchSubject(ex *MatchExpr) Type {
	st := c.checkExpr(ex.Subject)
	en, isEnum := st.(*tEnum)
	covered := map[string]bool{}
	hasWild := false
	var resultT Type
	for i := range ex.Arms {
		a := &ex.Arms[i]
		c.child()
		switch p := a.Pat.(type) {
		case *WildcardPat:
			hasWild = true
		case *VariantPat:
			if !isEnum {
				serr(patLine(p), "variant pattern %s on non-enum subject %s", p.Name, st)
			}
			v := findVariant(en.decl, p.Name)
			if v == nil {
				serr(patLine(p), "%s has no variant %s", en, p.Name)
			}
			if len(p.Bindings) != len(v.Fields) {
				serr(patLine(p), "%s has %d field(s), pattern binds %d", p.Name, len(v.Fields), len(p.Bindings))
			}
			covered[p.Name] = true
			for k, bd := range p.Bindings {
				if bd != "_" {
					c.cur.vars[bd] = c.fieldType(en, v, k)
				}
			}
		case *IdentPat:
			if isEnum && findVariant(en.decl, p.Name) != nil {
				v := findVariant(en.decl, p.Name)
				if len(v.Fields) > 0 {
					serr(patLine(p), "%s carries data; use %s(...) in the pattern", p.Name, p.Name)
				}
				covered[p.Name] = true
				c.patVariant[p] = true
			} else {
				c.cur.vars[p.Name] = st
			}
		case *LiteralPat:
			lt := c.checkExpr(p.X)
			if !sameType(lt, st) {
				serr(patLine(p), "pattern type %s does not match subject type %s", lt, st)
			}
		case *BoolPat:
			serr(patLine(p), "'if' arms need a subject-less match")
		default:
			serr(patLine(p), "channel arm in a match with a subject")
		}
		if a.Guard != nil {
			gt := c.checkExpr(a.Guard)
			if !sameType(gt, tbool) {
				serr(a.Line, "guard must be bool, got %s", gt)
			}
		}
		resultT = c.unify(resultT, c.checkArmBody(a), a.Line)
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
			serr(ex.Line, "non-exhaustive match on %s: missing %s", en, strings.Join(missing, ", "))
		}
	}
	if !isEnum && !hasWild {
		serr(ex.Line, "match on %s needs a _ arm", st)
	}
	if resultT == nil {
		resultT = tvoid
	}
	return resultT
}

func (c *checker) checkMatchSelect(ex *MatchExpr) Type {
	if ex.Subject != nil {
		serr(ex.Line, "channel arms need a subject-less match")
	}
	var resultT Type
	for i := range ex.Arms {
		a := &ex.Arms[i]
		c.child()
		switch p := a.Pat.(type) {
		case *RecvPat:
			ct := c.checkExpr(p.Chan)
			ch, ok := ct.(*tChan)
			if !ok {
				serr(patLine(p), "recv on non-channel %s", ct)
			}
			if p.Bind != "" && p.Bind != "_" {
				c.cur.vars[p.Bind] = ch.elem
			}
		case *SendPat:
			ct := c.checkExpr(p.Chan)
			ch, ok := ct.(*tChan)
			if !ok {
				serr(patLine(p), "send on non-channel %s", ct)
			}
			vt := c.checkExpr(p.Value)
			c.assignable(vt, ch.elem, patLine(p))
		case *AfterPat:
			dt := c.checkExpr(p.D)
			if !isNumeric(dt) {
				serr(patLine(p), "after() duration must be numeric, got %s", dt)
			}
		case *ClosedPat:
			serr(patLine(p), ".closed() arms are not supported in v2 (Go channels cannot peek)")
		case *WildcardPat:
		default:
			serr(patLine(p), "cannot mix channel arms with value/enum arms")
		}
		if a.Guard != nil {
			serr(a.Line, "guards on channel arms are not supported in v2")
		}
		resultT = c.unify(resultT, c.checkArmBody(a), a.Line)
		c.pop()
	}
	if resultT == nil {
		resultT = tvoid
	}
	return resultT
}

func (c *checker) checkMatchBool(ex *MatchExpr) Type {
	var resultT Type
	for i := range ex.Arms {
		a := &ex.Arms[i]
		c.child()
		switch p := a.Pat.(type) {
		case *BoolPat:
			bt := c.checkExpr(p.X)
			if !sameType(bt, tbool) {
				serr(patLine(p), "boolean arm must be bool, got %s", bt)
			}
		case *WildcardPat:
		default:
			serr(patLine(p), "subject-less match arms must be channel patterns, 'if' conditions, or '_'")
		}
		if a.Guard != nil {
			serr(a.Line, "guards on boolean arms are not supported")
		}
		resultT = c.unify(resultT, c.checkArmBody(a), a.Line)
		c.pop()
	}
	if resultT == nil {
		resultT = tvoid
	}
	return resultT
}
