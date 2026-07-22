package main

import (
	"fmt"
	"strconv"
	"strings"
)

// parse.go — go++ v2 compiler: recursive-descent parser.
// Consumes the token stream from lex.go and produces the AST from ast.go.
//
// Error handling is panic-mode recovery (§0: partial results on broken
// input): parseError panics unwind to the nearest recovery point —
// declaration, statement, or match arm — where the diagnostic is recorded
// and the parser synchronizes to the next boundary token. parse() returns
// everything it found; a parseError never escapes.

type parseError struct {
	line, col int
	msg       string
}

func (e parseError) Error() string { return e.msg }

type parser struct {
	toks []token
	pos  int
	diag *Diagnostics
	// noStructLit disables composite literals (Go's rule: they may not
	// appear between a keyword and the opening brace of its block, or
	// `if x {` would be misparsed). Set around condition parsing.
	noStructLit bool
}

func parse(toks []token) (f *File, diags *Diagnostics) {
	p := &parser{toks: toks, diag: &Diagnostics{}}
	defer func() {
		// last-resort net (e.g. a malformed `package` clause)
		if r := recover(); r != nil {
			if pe, ok := r.(parseError); ok {
				p.diag.errorfAt(pe.line, pe.col, "%s", pe.msg)
			} else {
				panic(r)
			}
		}
		diags = p.diag
	}()
	f = p.parseFile()
	return f, p.diag
}

func (p *parser) cur() token  { return p.toks[p.pos] }
func (p *parser) peek() token { return p.toks[p.pos+1] }

func (p *parser) next() token {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	return t
}

func (p *parser) skipNL() {
	for p.cur().kind == kNewline {
		p.next()
	}
}

func (p *parser) errorf(line int, format string, args ...any) {
	panic(parseError{line: line, msg: fmt.Sprintf(format, args...)})
}

// errorft is errorf with the offending token's column attached (§11).
func (p *parser) errorft(tk token, format string, args ...any) {
	panic(parseError{line: tk.line, col: tk.col, msg: fmt.Sprintf(format, args...)})
}

func (p *parser) expect(text string) {
	if p.cur().text != text {
		p.errorft(p.cur(), "expected %q, got %q", text, p.cur().text)
	}
	p.next()
}

// expectGT consumes the `>` closing a map<K, V> or chan<T> type. The lexer
// glues two nested closers into one `>>` operator token (ops2 in lex.go), so
// a type like map<int, map<int, bool>> ends in a single token: split it in
// place, leaving a ">" for the enclosing parseType to consume.
func (p *parser) expectGT() {
	if p.cur().text == ">>" {
		p.toks[p.pos].text = ">"
		return
	}
	p.expect(">")
}

func (p *parser) expectIdent() string {
	if p.cur().kind != kIdent {
		p.errorft(p.cur(), "expected identifier, got %q", p.cur().text)
	}
	return p.next().text
}

// ---------- file & declarations ----------

func (p *parser) parseFile() *File {
	p.skipNL()
	p.expect("package")
	f := &File{PkgName: p.expectIdent()}
	// imports come first, before any declaration (§3)
	for {
		p.skipNL()
		if p.cur().text != "import" {
			break
		}
		if imp := p.tryImport(); imp != nil {
			f.Imports = append(f.Imports, imp)
		}
	}
	for {
		p.skipNL()
		if p.cur().kind == kEOF {
			return f
		}
		if d := p.tryDecl(); d != nil {
			f.Decls = append(f.Decls, d)
		}
	}
}

// tryImport parses `import "dir/sub"`; on error it records the diagnostic
// and returns nil so the file keeps parsing.
func (p *parser) tryImport() (imp *ImportDecl) {
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(parseError); ok {
				p.diag.errorfAt(pe.line, pe.col, "%s", pe.msg)
				p.synchronizeDecl()
				imp = nil
				return
			}
			panic(r)
		}
	}()
	kw := p.cur()
	p.expect("import")
	tk := p.cur()
	if tk.kind != kString {
		p.errorft(tk, "expected import path string, got %q", tk.text)
	}
	p.next()
	path, err := strconv.Unquote(tk.text)
	if err != nil || path == "" || strings.HasPrefix(path, "/") {
		p.errorft(tk, "invalid import path %q (relative directory)", tk.text)
	}
	return &ImportDecl{Path: path, Line: kw.line}
}

// tryDecl parses one declaration; on a parse error it records the
// diagnostic, synchronizes to the next plausible declaration, and returns
// nil so the file keeps parsing.
func (p *parser) tryDecl() (d Decl) {
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(parseError); ok {
				p.diag.errorfAt(pe.line, pe.col, "%s", pe.msg)
				p.synchronizeDecl()
				d = nil
				return
			}
			panic(r)
		}
	}()
	switch p.cur().text {
	case "func", "fn":
		return p.parseFuncDecl()
	case "enum":
		return p.parseEnumDecl()
	case "type":
		return p.parseStructDecl()
	case "behavior":
		return p.parseBehaviorDecl()
	case "actor":
		return p.parseActorDecl()
	case "impl":
		return p.parseImplDecl()
	case "comptime":
		return p.parseComptimeDecl()
	case "import":
		p.errorft(p.cur(), "imports must come before declarations")
	}
	p.errorft(p.cur(), "expected declaration, got %q", p.cur().text)
	return nil
}

// parseComptimeDecl parses a top-level `comptime { ... }` block (§10
// metaprogramming): the statement list is ordinary syntax, restricted by
// the comptime interpreter at evaluation time.
func (p *parser) parseComptimeDecl() Decl {
	tk := p.next() // comptime
	if p.cur().text != "{" {
		p.errorft(p.cur(), "expected { after comptime at top level (comptime expr lives inside functions)")
	}
	return &ComptimeDecl{Body: p.parseBlock(), Line: tk.line, Col: tk.col}
}

// synchronizeDecl skips tokens until something that can start a
// declaration at depth 0, or EOF. It always advances at least one token.
func (p *parser) synchronizeDecl() {
	start := p.pos
	depth := 0
	for p.cur().kind != kEOF {
		if p.pos > start && depth == 0 {
			switch p.cur().text {
			case "func", "fn", "enum", "type", "import", "comptime", "behavior", "impl", "actor":
				return
			}
		}
		switch p.cur().text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			if depth > 0 {
				depth--
			}
		}
		p.next()
	}
}

// synchronizeStmt skips to the next statement boundary: a newline
// (consumed), a closing brace (left for the block), or EOF.
func (p *parser) synchronizeStmt() {
	for {
		tk := p.cur()
		switch {
		case tk.kind == kEOF:
			return
		case tk.kind == kNewline:
			p.next()
			return
		case tk.text == "}":
			return
		}
		p.next()
	}
}

func (p *parser) parseFuncDecl() Decl {
	tk := p.next() // func / fn
	name := p.expectIdent()
	var typeParams, bounds []string
	if p.cur().text == "[" { // func Identity[T](x T) T (§8)
		p.next()
		for {
			typeParams = append(typeParams, p.expectIdent())
			bound := ""
			if p.cur().text == ":" { // T: Stringer — a behavior bound (§8)
				p.next()
				bound = p.expectIdent()
			}
			bounds = append(bounds, bound)
			if p.cur().text == "," {
				p.next()
				continue
			}
			break
		}
		p.expect("]")
	}
	p.expect("(")
	params := p.parseFieldList(")")
	var results []Field
	if p.cur().text == "(" {
		p.next()
		results = p.parseFieldList(")")
	} else if p.cur().text != "{" && p.cur().text != "=" && p.cur().kind != kNewline {
		results = []Field{{Type: p.parseType(), Line: p.cur().line}}
	}
	p.skipNL()
	if p.cur().text == "=" { // func F(...) ... = native — stdlib FFI
		p.next()
		if kw := p.expectIdent(); kw != "native" {
			p.errorft(tk, "expected `native` after =, got %q", kw)
		}
		return &FuncDecl{Name: name, TypeParams: typeParams, Bounds: bounds, Params: params, Results: results, Native: true, Line: tk.line, Col: tk.col}
	}
	body := p.parseBlock()
	return &FuncDecl{Name: name, TypeParams: typeParams, Bounds: bounds, Params: params, Results: results, Body: body, Line: tk.line, Col: tk.col}
}

// parseActorDecl parses `actor Name { fields; be Method(args) { } }`.
func (p *parser) parseActorDecl() Decl {
	tk := p.next() // actor
	d := &ActorDecl{Name: p.expectIdent(), Line: tk.line, Col: tk.col}
	p.expect("{")
	for {
		p.skipNL()
		if p.cur().text == "}" {
			p.next()
			return d
		}
		if p.cur().text == "be" {
			mk := p.next()
			fn := &FuncDecl{Name: p.expectIdent(), Line: mk.line, Col: mk.col}
			p.expect("(")
			fn.Params = p.parseFieldList(")")
			if p.cur().text == "(" {
				p.next()
				fn.Results = p.parseFieldList(")")
			} else if p.cur().text != "{" && p.cur().kind != kNewline {
				fn.Results = []Field{{Type: p.parseType(), Line: p.cur().line}}
			}
			p.skipNL()
			fn.Body = p.parseBlock()
			d.Methods = append(d.Methods, fn)
			continue
		}
		// a field: names + type (same shape as struct fields)
		fline := p.cur().line
		names := []string{p.expectIdent()}
		for p.cur().text == "," {
			p.next()
			names = append(names, p.expectIdent())
		}
		ty := p.parseType()
		for _, n := range names {
			d.Fields = append(d.Fields, Field{Name: n, Type: ty, Line: fline})
		}
		if p.cur().text == ";" {
			p.next()
		}
	}
}

// parseBehaviorDecl parses `behavior Name { Method(self) Result ... }`.
func (p *parser) parseBehaviorDecl() Decl {
	tk := p.next() // behavior
	d := &BehaviorDecl{Name: p.expectIdent(), Line: tk.line, Col: tk.col}
	p.expect("{")
	for {
		p.skipNL()
		if p.cur().text == "}" {
			p.next()
			return d
		}
		mk := p.cur()
		m := BehaviorMethod{Name: p.expectIdent(), Line: mk.line, Col: mk.col}
		p.expect("(")
		m.Params = p.parseFieldList(")")
		if p.cur().text == "(" {
			p.next()
			m.Results = p.parseFieldList(")")
		} else if p.cur().kind == kIdent || p.cur().text == "[" || p.cur().text == "*" ||
			p.cur().text == "map" || p.cur().text == "chan" {
			m.Results = []Field{{Type: p.parseType(), Line: p.cur().line}}
		}
		save := p.pos
		p.skipNL()
		if p.cur().text == "{" { // default implementation (§23-lite)
			m.Body = p.parseBlock()
		} else {
			p.pos = save
		}
		d.Methods = append(d.Methods, m)
	}
}

// parseImplDecl parses `impl Behavior for Type { Method(self) ... { } }`.
func (p *parser) parseImplDecl() Decl {
	tk := p.next() // impl
	d := &ImplDecl{Behavior: p.expectIdent(), Line: tk.line, Col: tk.col}
	if p.cur().text != "for" {
		p.errorft(p.cur(), "expected `for` after the behavior name, got %q", p.cur().text)
	}
	p.next()
	d.Type = p.parseType()
	p.expect("{")
	for {
		p.skipNL()
		if p.cur().text == "}" {
			p.next()
			return d
		}
		mk := p.cur()
		fn := &FuncDecl{Name: p.expectIdent(), Line: mk.line, Col: mk.col}
		p.expect("(")
		fn.Params = p.parseFieldList(")")
		if p.cur().text == "(" {
			p.next()
			fn.Results = p.parseFieldList(")")
		} else if p.cur().text != "{" && p.cur().text != "=" && p.cur().kind != kNewline {
			fn.Results = []Field{{Type: p.parseType(), Line: p.cur().line}}
		}
		p.skipNL()
		fn.Body = p.parseBlock()
		d.Methods = append(d.Methods, fn)
	}
}

// parseFieldList parses `a int, b int` / `a, b int` / `int` style lists
// up to (but not consuming) the closer token.
func (p *parser) parseFieldList(closer string) []Field {
	var fields []Field
	for p.cur().text != closer {
		// lookahead: gather a comma-separated ident chain
		idx := p.pos
		for p.toks[idx].kind == kIdent && p.toks[idx+1].text == "," {
			idx += 2
		}
		if p.toks[idx].kind == kIdent && p.isTypeStart(idx+1) {
			// names sharing one type: a, b int
			var names []string
			for i := p.pos; i <= idx; i += 2 {
				names = append(names, p.toks[i].text)
			}
			p.pos = idx + 1
			ty := p.parseType()
			for _, n := range names {
				fields = append(fields, Field{Name: n, Type: ty, Line: p.cur().line})
			}
		} else {
			// bare type field
			fields = append(fields, Field{Type: p.parseType(), Line: p.cur().line})
		}
		if p.cur().text == "," {
			p.next()
		} else {
			break
		}
	}
	p.expect(closer)
	return fields
}

func (p *parser) isTypeStart(i int) bool {
	return p.toks[i].kind == kIdent || p.toks[i].text == "[" || p.toks[i].text == "*"
}

func (p *parser) parseEnumDecl() Decl {
	line := p.next().line // enum
	name := p.expectIdent()
	var tps []string
	if p.cur().text == "[" {
		p.next()
		for {
			tps = append(tps, p.expectIdent())
			if p.cur().text == "," {
				p.next()
				continue
			}
			break
		}
		p.expect("]")
	}
	p.skipNL()
	p.expect("{")
	var vars []Variant
	for {
		p.skipNL()
		if p.cur().text == "}" {
			p.next()
			break
		}
		vline := p.cur().line
		vname := p.expectIdent()
		var fields []Field
		if p.cur().text == "(" {
			p.next()
			fields = p.parseFieldList(")")
		}
		vars = append(vars, Variant{Name: vname, Fields: fields, Line: vline})
		if p.cur().text == ";" || p.cur().text == "," {
			p.next()
		}
	}
	return &EnumDecl{Name: name, TypeParams: tps, Variants: vars, Line: line}
}

func (p *parser) parseStructDecl() Decl {
	line := p.next().line // type
	name := p.expectIdent()
	var typeParams []string
	if p.cur().text == "[" { // type Pair[T] struct (§8)
		p.next()
		for {
			typeParams = append(typeParams, p.expectIdent())
			if p.cur().text == "," {
				p.next()
				continue
			}
			break
		}
		p.expect("]")
	}
	if p.cur().text != "struct" {
		p.errorft(p.cur(), "only struct types are supported, got %q", p.cur().text)
	}
	p.next()
	p.skipNL()
	p.expect("{")
	var fields []Field
	for {
		p.skipNL()
		if p.cur().text == "}" {
			p.next()
			break
		}
		if p.cur().kind == kEOF {
			p.diag.errorf(line, "unterminated struct declaration")
			break
		}
		fline := p.cur().line
		names := []string{p.expectIdent()}
		for p.cur().text == "," {
			p.next()
			names = append(names, p.expectIdent())
		}
		ty := p.parseType()
		for _, n := range names {
			fields = append(fields, Field{Name: n, Type: ty, Line: fline})
		}
		if p.cur().text == ";" {
			p.next()
		}
	}
	return &StructDecl{Name: name, TypeParams: typeParams, Fields: fields, Line: line}
}

// ---------- types ----------

func (p *parser) parseType() TypeExpr {
	tk := p.cur()
	line := tk.line
	col := tk.col
	switch tk.text {
	case "map":
		p.next()
		if p.cur().text == "[" {
			p.errorft(p.cur(), "map types are written map<K, V> (e.g. map<string, int>)")
		}
		p.expect("<")
		k := p.parseType()
		p.expect(",")
		v := p.parseType()
		p.expectGT()
		return &MapType{K: k, V: v, Line: line, Col: col}
	case "chan":
		p.next()
		if p.cur().text == "[" {
			p.errorft(p.cur(), "chan types are written chan<T> (e.g. chan<int>)")
		}
		p.expect("<")
		e := p.parseType()
		p.expectGT()
		return &ChanType{Elem: e, Line: line, Col: col}
	case "[":
		p.next()
		if p.cur().text == "]" { // Go's []T — rejected with guidance
			p.errorft(p.cur(), "slice types are written [T] (e.g. [int])")
		}
		elem := p.parseType()
		p.expect("]")
		return &SliceType{Elem: elem, Line: line, Col: col}
	case "*":
		p.next()
		return &StarType{X: p.parseType(), Line: line, Col: col}
	}
	if tk.kind == kIdent {
		p.next()
		var t TypeExpr = &IdentType{Name: tk.text, Line: line, Col: col}
		if p.cur().text == "." { // pkg.Type
			p.next()
			t = &IdentType{Name: tk.text + "." + p.expectIdent(), Line: line, Col: col}
		}
		if p.cur().text == "[" { // generic instantiation
			p.next()
			var args []TypeExpr
			for {
				args = append(args, p.parseType())
				if p.cur().text == "," {
					p.next()
					continue
				}
				break
			}
			p.expect("]")
			t = &IndexType{X: t, Args: args, Line: line, Col: col}
		}
		return t
	}
	p.errorf(line, "expected type, got %q", tk.text)
	return nil
}

// ---------- statements ----------

func (p *parser) parseBlock() *Block {
	line := p.cur().line
	p.expect("{")
	var list []Stmt
	for {
		p.skipNL()
		if p.cur().text == "}" {
			p.next()
			return &Block{List: list, Line: line}
		}
		if p.cur().kind == kEOF {
			p.diag.errorf(line, "unterminated block")
			return &Block{List: list, Line: line}
		}
		if s := p.tryStmt(); s != nil {
			list = append(list, s)
		}
	}
}

// tryStmt parses one statement; on a parse error it records the
// diagnostic, synchronizes to the next statement boundary, and returns nil.
func (p *parser) tryStmt() (s Stmt) {
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(parseError); ok {
				p.diag.errorfAt(pe.line, pe.col, "%s", pe.msg)
				p.synchronizeStmt()
				s = nil
				return
			}
			panic(r)
		}
	}()
	return p.parseStmt()
}

func (p *parser) parseStmt() Stmt {
	tk := p.cur()
	switch tk.text {
	case "{":
		return p.parseBlock()
	case "if":
		return p.parseIf()
	case "for":
		return p.parseFor()
	case "loop":
		p.next()
		return &LoopStmt{Body: p.parseBlock(), Line: tk.line, Col: tk.col}
	case "break":
		p.next()
		label := ""
		if p.cur().kind == kIdent {
			label = p.next().text
		}
		return &BreakStmt{Label: label, Line: tk.line, Col: tk.col}
	case "return":
		p.next()
		var res []Expr
		if p.cur().kind != kNewline && p.cur().text != "}" {
			res = p.parseExprList()
		}
		return &ReturnStmt{Results: res, Line: tk.line, Col: tk.col}
	case "var":
		return p.parseVar()
	case "select", "switch", "case":
		p.errorft(tk, "%q was removed in go++ — use match", tk.text)
	case "continue":
		tk := p.next()
		return &ContinueStmt{Line: tk.line, Col: tk.col}
	case "defer":
		tk := p.next()
		x := p.parseExpr(1)
		if _, isCall := x.(*CallExpr); !isCall {
			p.errorft(tk, "defer requires a call expression")
		}
		return &DeferStmt{X: x, Line: tk.line, Col: tk.col}
	case "go", "const", "type", "import", "goto":
		p.errorft(tk, "%q is not supported in v2 yet", tk.text)
	}
	lhs := p.parseExprList()
	switch p.cur().text {
	case ":=", "=", "+=", "-=", "*=", "/=":
		op := p.next().text
		return &AssignStmt{Lhs: lhs, Op: op, Rhs: p.parseExprList(), Line: tk.line, Col: tk.col}
	case "++", "--":
		op := p.next().text
		return &IncDecStmt{X: lhs[0], Op: op, Line: tk.line, Col: tk.col}
	}
	if len(lhs) > 1 {
		p.errorft(tk, "unexpected expression list in statement")
	}
	return &ExprStmt{X: lhs[0], Line: tk.line, Col: tk.col}
}

func (p *parser) parseVar() Stmt {
	tk := p.next() // var
	name := p.expectIdent()
	ty := p.parseType()
	var init Expr
	if p.cur().text == "=" {
		p.next()
		init = p.parseExpr(1)
	}
	return &VarStmt{Name: name, Type: ty, Init: init, Line: tk.line, Col: tk.col}
}

func (p *parser) parseIf() Stmt {
	tk := p.next() // if
	cond := p.parseCond()
	then := p.parseBlock()
	var els Stmt
	save := p.pos
	p.skipNL()
	if p.cur().text == "else" {
		p.next()
		p.skipNL()
		if p.cur().text == "if" {
			els = p.parseIf()
		} else {
			els = p.parseBlock()
		}
	} else {
		p.pos = save
	}
	return &IfStmt{Cond: cond, Then: then, Else: els, Line: tk.line, Col: tk.col}
}

// maybeInterp splits a string token containing {expr} interpolations
// into a StringInterpExpr; ok=false means plain string (no braces).
func (p *parser) maybeInterp(tk token) (Expr, bool) {
	inner := tk.text[1 : len(tk.text)-1]
	if !strings.ContainsAny(inner, "{}") {
		return nil, false
	}
	var parts []Expr
	lit := func(s string) {
		if s != "" {
			parts = append(parts, &BasicLit{Kind: kString, Value: "\"" + s + "\"", Line: tk.line, Col: tk.col})
		}
	}
	var cur strings.Builder
	for i := 0; i < len(inner); {
		c := inner[i]
		switch {
		case c == '\\' && i+1 < len(inner):
			cur.WriteByte(c)
			cur.WriteByte(inner[i+1])
			i += 2
		case c == '{' && i+1 < len(inner) && inner[i+1] == '{':
			cur.WriteByte('{')
			i += 2
		case c == '{':
			// find the matching }, tracking nesting and inner strings
			depth, j := 1, i+1
			for ; j < len(inner) && depth > 0; j++ {
				switch inner[j] {
				case '{':
					depth++
				case '}':
					depth--
				case '"':
					for j++; j < len(inner) && inner[j] != '"'; j++ {
						if inner[j] == '\\' {
							j++
						}
					}
				}
			}
			if depth != 0 {
				p.errorft(tk, "unclosed { in string interpolation")
			}
			frag := inner[i+1 : j-1]
			if strings.TrimSpace(frag) == "" {
				p.errorft(tk, "empty interpolation {}")
			}
			lit(cur.String())
			cur.Reset()
			parts = append(parts, p.parseInterpFrag(frag, tk))
			i = j
		case c == '}' && i+1 < len(inner) && inner[i+1] == '}':
			cur.WriteByte('}')
			i += 2
		case c == '}':
			p.errorft(tk, "unmatched } in string (write it as }})")
		default:
			cur.WriteByte(c)
			i++
		}
	}
	lit(cur.String())
	if len(parts) == 0 {
		return nil, false
	}
	return &StringInterpExpr{Parts: parts, Line: tk.line, Col: tk.col}, true
}

// parseInterpFrag parses one {expr} interpolation fragment.
func (p *parser) parseInterpFrag(frag string, tk token) Expr {
	toks, err := lexAt(frag, tk.line-1)
	if err != nil {
		p.errorft(tk, "bad interpolation {%s}: %s", frag, err)
	}
	fp := &parser{toks: toks, diag: p.diag}
	e := fp.parseExpr(1)
	if fp.cur().kind != kEOF {
		p.errorft(tk, "bad interpolation {%s}: trailing %q", frag, fp.cur().text)
	}
	return e
}

// parseForInExpr parses the ranged-over expression of a for-in loop,
// keeping the body's { safe from struct-literal parsing.
func (p *parser) parseForInExpr() Expr {
	save := p.noStructLit
	p.noStructLit = true
	x := p.parseExpr(1)
	p.noStructLit = save
	return x
}

func (p *parser) parseFor() Stmt {
	tk := p.next() // for
	line, col := tk.line, tk.col
	if p.cur().text == "{" {
		return &ForStmt{Body: p.parseBlock(), Line: line, Col: col}
	}
	// for x in expr { } / for i, x in expr { } — range loops (runtime)
	// and comptime iteration (§10)
	if p.cur().kind == kIdent && p.peek().text == "in" {
		v := p.next().text
		p.next() // in
		x := p.parseForInExpr()
		return &ForInStmt{Var: v, X: x, Body: p.parseBlock(), Line: line, Col: col}
	}
	if p.cur().kind == kIdent && p.peek().text == "," &&
		p.toks[p.pos+2].kind == kIdent && p.toks[p.pos+3].text == "in" {
		v := p.next().text
		p.next() // ,
		v2 := p.next().text
		p.next() // in
		x := p.parseForInExpr()
		return &ForInStmt{Var: v, Var2: v2, X: x, Body: p.parseBlock(), Line: line, Col: col}
	}
	save := p.noStructLit
	p.noStructLit = true
	first := p.parseExprList()
	p.noStructLit = save
	if p.cur().text == "{" {
		return &ForStmt{Cond: first[0], Body: p.parseBlock(), Line: line, Col: col}
	}
	var init Stmt
	if op := p.cur().text; op == ":=" || op == "=" {
		p.next()
		init = &AssignStmt{Lhs: first, Op: op, Rhs: p.parseExprList(), Line: line, Col: col}
	} else {
		init = &ExprStmt{X: first[0], Line: line, Col: col}
	}
	p.expect(";")
	var cond Expr
	if p.cur().text != ";" {
		cond = p.parseExpr(1)
	}
	p.expect(";")
	var post Stmt
	if p.cur().text != "{" {
		e := p.parseExprList()
		switch p.cur().text {
		case "++", "--":
			op := p.next().text
			post = &IncDecStmt{X: e[0], Op: op, Line: line, Col: col}
		case "=", ":=":
			op := p.next().text
			post = &AssignStmt{Lhs: e, Op: op, Rhs: p.parseExprList(), Line: line, Col: col}
		default:
			post = &ExprStmt{X: e[0], Line: line, Col: col}
		}
	}
	return &ForStmt{Init: init, Cond: cond, Post: post, Body: p.parseBlock(), Line: line, Col: col}
}

// ---------- match ----------

func (p *parser) parseMatch() Expr {
	tk := p.next() // match
	line := tk.line
	fair := false
	if p.cur().text == "." && p.peek().text == "fair" {
		p.next()
		p.next()
		fair = true
	}
	var subj Expr
	if p.cur().text != "{" {
		subj = p.parseCond()
	}
	p.expect("{")
	var arms []MatchArm
	for {
		p.skipNL()
		if p.cur().text == "}" {
			p.next()
			break
		}
		if p.cur().kind == kEOF {
			p.diag.errorf(line, "unterminated match")
			break
		}
		if a, ok := p.tryArm(); ok {
			arms = append(arms, a)
		}
	}
	return &MatchExpr{Subject: subj, Arms: arms, Fair: fair, Line: line, Col: tk.col}
}

// tryArm parses one match arm; on a parse error it records the diagnostic,
// synchronizes to the next arm boundary, and returns false.
func (p *parser) tryArm() (a MatchArm, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(parseError); ok {
				p.diag.errorfAt(pe.line, pe.col, "%s", pe.msg)
				p.synchronizeStmt()
				ok = false
				return
			}
			panic(r)
		}
	}()
	return p.parseArm(), true
}

func (p *parser) parseArm() MatchArm {
	tk := p.cur()
	pat := p.parsePattern()
	var guard Expr
	if p.cur().text == "if" {
		p.next()
		guard = p.parseExpr(1)
	}
	p.expect("->")
	p.skipNL()
	a := MatchArm{Pat: pat, Guard: guard, Line: tk.line, Col: tk.col}
	if p.cur().text == "{" {
		a.Body = p.parseBlock().List
	} else {
		a.BodyExpr = p.parseExpr(1)
	}
	return a
}

func (p *parser) parsePattern() Pattern {
	tk := p.cur()
	line := tk.line
	col := tk.col
	if tk.text == "if" {
		p.next()
		return &BoolPat{X: p.parseExpr(1), Line: line, Col: col}
	}
	if tk.kind == kIdent && p.peek().text == ":=" {
		bind := p.next().text
		p.next() // :=
		e := p.parseExpr(1)
		if c, ok := e.(*CallExpr); ok && len(c.Args) == 0 {
			if sel, ok := c.Fun.(*SelectorExpr); ok && sel.Sel == "recv" {
				return &RecvPat{Bind: bind, Chan: sel.X, Line: line, Col: col}
			}
		}
		p.errorf(line, "recv arm must look like x := ch.recv()")
	}
	if tk.text == "_" {
		p.next()
		return &WildcardPat{Line: line, Col: col}
	}
	if tk.text == "after" && p.peek().text == "(" {
		p.next()
		p.next()
		d := p.parseExpr(1)
		p.expect(")")
		return &AfterPat{D: d, Line: line, Col: col}
	}
	e := p.parseUnary()
	// channel-shaped patterns
	if c, ok := e.(*CallExpr); ok {
		if sel, ok := c.Fun.(*SelectorExpr); ok {
			switch sel.Sel {
			case "send":
				if len(c.Args) != 1 {
					p.errorf(line, "send arm needs exactly one value")
				}
				return &SendPat{Chan: sel.X, Value: c.Args[0], Line: line, Col: col}
			case "closed":
				return &ClosedPat{Chan: sel.X, Line: line, Col: col}
			}
		}
		// Variant( bindings ) — destructuring
		if id, ok := c.Fun.(*Ident); ok {
			var bindings []string
			allIdent := true
			for _, arg := range c.Args {
				if bi, ok := arg.(*Ident); ok {
					bindings = append(bindings, bi.Name)
				} else {
					allIdent = false
				}
			}
			if allIdent {
				return &VariantPat{Name: id.Name, Bindings: bindings, Line: line, Col: col}
			}
		}
	}
	if id, ok := e.(*Ident); ok {
		// bare name: unit variant or subject binding — sema disambiguates
		return &IdentPat{Name: id.Name, Line: line, Col: col}
	}
	return &LiteralPat{X: e, Line: line, Col: col}
}

// ---------- expressions ----------

var binPrec = map[string]int{
	"||": 1,
	"&&": 2,
	"==": 3, "!=": 3, "<": 3, "<=": 3, ">": 3, ">=": 3,
	"+": 4, "-": 4, "|": 4, "^": 4,
	"*": 5, "/": 5, "%": 5, "<<": 5, ">>": 5, "&": 5, "&^": 5,
}

func (p *parser) parseExprList() []Expr {
	list := []Expr{p.parseExpr(1)}
	for p.cur().text == "," {
		p.next()
		list = append(list, p.parseExpr(1))
	}
	return list
}

func (p *parser) parseExpr(minPrec int) Expr {
	x := p.parseUnary()
	for {
		tk := p.cur()
		if tk.kind == kNewline || tk.kind == kEOF {
			break
		}
		prec, ok := binPrec[tk.text]
		if !ok || prec < minPrec {
			break
		}
		p.next()
		y := p.parseExpr(prec + 1)
		x = &BinaryExpr{Op: tk.text, X: x, Y: y, Line: tk.line, Col: tk.col}
	}
	return x
}

func (p *parser) parseUnary() Expr {
	tk := p.cur()
	switch tk.text {
	case "-", "!", "+", "^", "&", "*":
		p.next()
		return &UnaryExpr{Op: tk.text, X: p.parseUnary(), Line: tk.line, Col: tk.col}
	case "<-":
		// spec §5 removal: bare channel receive is gone, use ch.recv()
		p.errorft(tk, "<- was removed in go++ — use ch.recv() and ch.send(v)")
		p.next()
		return p.parseUnary()
	}
	return p.parsePostfix()
}

func (p *parser) parsePostfix() Expr {
	x := p.parsePrimary()
	for {
		tk := p.cur()
		switch tk.text {
		case ".":
			p.next()
			name := p.cur()
			x = &SelectorExpr{X: x, Sel: p.expectIdent(), Line: name.line, Col: name.col}
		case "(":
			x = &CallExpr{Fun: x, Args: p.parseCallArgs(), Line: tk.line, Col: tk.col}
		case "[":
			p.next()
			// x[i] indexing, or x[low:high] slicing (either side omittable)
			var low, high Expr
			if p.cur().text != ":" && p.cur().text != "]" {
				low = p.parseExpr(1)
			}
			if p.cur().text == ":" {
				p.next()
				if p.cur().text != "]" {
					high = p.parseExpr(1)
				}
				p.expect("]")
				x = &SliceExpr{X: x, Low: low, High: high, Line: tk.line, Col: tk.col}
				continue
			}
			if low == nil {
				p.errorft(p.cur(), "expected an index or a slice range")
			}
			idx := []Expr{low}
			for p.cur().text == "," {
				p.next()
				idx = append(idx, p.parseExpr(1))
			}
			p.expect("]")
			x = &IndexExpr{X: x, Index: idx, Line: tk.line, Col: tk.col}
		case "?":
			p.next()
			x = &TryExpr{X: x, Line: tk.line, Col: tk.col}
		case "{":
			if p.noStructLit {
				return x
			}
			x = p.parseStructLit(x)
		default:
			return x
		}
	}
}

// parseStructLit parses `{ Name: v, ... }` / `{ v, ... }` after a type
// name in expression position — plain (Point) or generic (Pair[int]).
func (p *parser) parseStructLit(x Expr) Expr {
	line := p.cur().line
	col := p.cur().col
	te := exprToType(x)
	if te == nil {
		p.errorf(line, "expected a struct type name before {")
	}
	p.next() // {
	var fields []FieldVal
	for {
		p.skipNL()
		if p.cur().text == "}" {
			p.next()
			break
		}
		fl := p.cur().line
		fc := p.cur().col
		if p.cur().kind == kIdent && p.peek().text == ":" {
			name := p.next().text
			p.next() // :
			v := p.parseExpr(1)
			fields = append(fields, FieldVal{Name: name, Value: v, Line: fl, Col: fc})
		} else {
			v := p.parseExpr(1)
			fields = append(fields, FieldVal{Value: v, Line: fl, Col: fc})
		}
		if p.cur().text == "," {
			p.next()
		}
	}
	return &StructLitExpr{Type: te, Fields: fields, Line: line, Col: col}
}

// parseCond parses a condition expression with composite literals
// disabled (Go's ambiguity rule).
func (p *parser) parseCond() Expr {
	save := p.noStructLit
	p.noStructLit = true
	defer func() { p.noStructLit = save }()
	return p.parseExpr(1)
}

func (p *parser) parseCallArgs() []Expr {
	p.expect("(")
	var args []Expr
	if p.cur().text != ")" {
		args = p.parseExprList()
	}
	p.expect(")")
	return args
}

func (p *parser) parsePrimary() Expr {
	tk := p.cur()
	switch {
	case tk.kind == kInt || tk.kind == kFloat || tk.kind == kString || tk.kind == kRune:
		p.next()
		if tk.kind == kString {
			if e, ok := p.maybeInterp(tk); ok {
				return e
			}
		}
		return &BasicLit{Kind: tk.kind, Value: tk.text, Line: tk.line, Col: tk.col}
	case tk.kind == kIdent:
		switch tk.text {
		case "match":
			return p.parseMatch()
		case "comptime":
			p.next()
			return &ComptimeExpr{X: p.parseExpr(1), Line: tk.line, Col: tk.col}
		case "map":
			// map literal: map<string, int>{"a": 1} (composite, keyed)
			line := p.next().line
			col := tk.col
			if p.cur().text == "[" {
				p.errorft(p.cur(), "map types are written map<K, V> (e.g. map<string, int>)")
			}
			p.expect("<")
			k := p.parseType()
			p.expect(",")
			v := p.parseType()
			p.expectGT()
			if p.cur().text != "{" {
				p.errorf(line, "map literal map<K, V> needs {entries}")
			}
			p.next()
			var entries []MapEntry
			for {
				p.skipNL()
				if p.cur().text == "}" {
					p.next()
					break
				}
				key := p.parseExpr(1)
				p.expect(":")
				val := p.parseExpr(1)
				entries = append(entries, MapEntry{Key: key, Value: val, Line: lineOf(key)})
				p.skipNL()
				if p.cur().text == "," {
					p.next()
					continue
				}
				p.expect("}")
				break
			}
			return &MapLitExpr{K: k, V: v, Entries: entries, Line: line, Col: col}
		case "chan":
			line := p.next().line
			col := tk.col
			if p.cur().text == "[" {
				p.errorft(p.cur(), "chan types are written chan<T> (e.g. chan<int>)")
			}
			p.expect("<")
			elem := p.parseType()
			p.expectGT()
			if p.cur().text != "(" {
				p.errorf(line, "chan<T> needs (cap) in expression position")
			}
			p.next()
			var capE Expr
			if p.cur().text != ")" {
				capE = p.parseExpr(1)
			}
			p.expect(")")
			return &MakeChanExpr{Elem: elem, Cap: capE, Line: line, Col: col}
		}
		p.next()
		return &Ident{Name: tk.text, Line: tk.line, Col: tk.col}
	case tk.text == "(":
		p.next()
		e := p.parseExpr(1)
		p.expect(")")
		return e
	case tk.text == "[":
		// a [ at expression start can only open a slice literal — an
		// index expression needs a leading operand
		return p.parseSliceLit()
	}
	p.errorft(tk, "expected expression, got %q", tk.text)
	return nil
}

// parseSliceLit parses `[T]{v, ...}` — the `[` is unambiguous at
// expression start. Values are positional only (cf. parseStructLit).
func (p *parser) parseSliceLit() Expr {
	line := p.cur().line
	col := p.cur().col
	p.next()                 // [
	if p.cur().text == "]" { // Go's []T — rejected with guidance
		p.errorft(p.cur(), "slice types are written [T] (e.g. [int])")
	}
	elem := p.parseType()
	p.expect("]")
	if p.cur().text != "{" {
		p.errorf(line, "slice literal [T] needs {values}")
	}
	p.next() // {
	var values []Expr
	for {
		p.skipNL()
		if p.cur().text == "}" {
			p.next()
			break
		}
		values = append(values, p.parseExpr(1))
		p.skipNL()
		if p.cur().text == "," {
			p.next()
			continue
		}
		p.expect("}")
		break
	}
	return &SliceLitExpr{Elem: elem, Values: values, Line: line, Col: col}
}
