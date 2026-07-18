package main

import "fmt"

// parse.go — go++ v2 compiler: recursive-descent parser.
// Consumes the token stream from lex.go and produces the AST from ast.go.
// Errors are raised via panic(parseError) and recovered in parse().

type parseError struct{ msg string }

func (e parseError) Error() string { return e.msg }

type parser struct {
	toks []token
	pos  int
}

func parse(toks []token) (f *File, err error) {
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(parseError); ok {
				err = pe
				return
			}
			panic(r)
		}
	}()
	p := &parser{toks: toks}
	f = p.parseFile()
	return f, nil
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
	panic(parseError{fmt.Sprintf("line %d: %s", line, fmt.Sprintf(format, args...))})
}

func (p *parser) expect(text string) {
	if p.cur().text != text {
		p.errorf(p.cur().line, "expected %q, got %q", text, p.cur().text)
	}
	p.next()
}

func (p *parser) expectIdent() string {
	if p.cur().kind != kIdent {
		p.errorf(p.cur().line, "expected identifier, got %q", p.cur().text)
	}
	return p.next().text
}

// ---------- file & declarations ----------

func (p *parser) parseFile() *File {
	p.skipNL()
	p.expect("package")
	f := &File{PkgName: p.expectIdent()}
	for {
		p.skipNL()
		if p.cur().kind == kEOF {
			return f
		}
		switch p.cur().text {
		case "func", "fn":
			f.Decls = append(f.Decls, p.parseFuncDecl())
		case "enum":
			f.Decls = append(f.Decls, p.parseEnumDecl())
		case "import":
			p.errorf(p.cur().line, "import is not supported in v2 yet")
		default:
			p.errorf(p.cur().line, "expected declaration, got %q", p.cur().text)
		}
	}
}

func (p *parser) parseFuncDecl() Decl {
	line := p.next().line // func / fn
	name := p.expectIdent()
	p.expect("(")
	params := p.parseFieldList(")")
	var results []Field
	if p.cur().text == "(" {
		p.next()
		results = p.parseFieldList(")")
	} else if p.cur().text != "{" && p.cur().kind != kNewline {
		results = []Field{{Type: p.parseType(), Line: p.cur().line}}
	}
	p.skipNL()
	body := p.parseBlock()
	return &FuncDecl{Name: name, Params: params, Results: results, Body: body, Line: line}
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

// ---------- types ----------

func (p *parser) parseType() TypeExpr {
	tk := p.cur()
	line := tk.line
	switch tk.text {
	case "map":
		p.next()
		p.expect("[")
		k := p.parseType()
		p.expect("]")
		return &MapType{K: k, V: p.parseType(), Line: line}
	case "chan":
		p.next()
		p.expect("[")
		e := p.parseType()
		p.expect("]")
		return &ChanType{Elem: e, Line: line}
	case "[":
		p.next()
		p.expect("]")
		return &SliceType{Elem: p.parseType(), Line: line}
	case "*":
		p.next()
		return &StarType{X: p.parseType(), Line: line}
	}
	if tk.kind == kIdent {
		p.next()
		var t TypeExpr = &IdentType{Name: tk.text, Line: line}
		if p.cur().text == "." { // pkg.Type
			p.next()
			t = &IdentType{Name: tk.text + "." + p.expectIdent(), Line: line}
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
			t = &IndexType{X: t, Args: args, Line: line}
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
			p.errorf(p.cur().line, "unterminated block")
		}
		list = append(list, p.parseStmt())
	}
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
		return &LoopStmt{Body: p.parseBlock(), Line: tk.line}
	case "break":
		p.next()
		label := ""
		if p.cur().kind == kIdent {
			label = p.next().text
		}
		return &BreakStmt{Label: label, Line: tk.line}
	case "return":
		p.next()
		var res []Expr
		if p.cur().kind != kNewline && p.cur().text != "}" {
			res = p.parseExprList()
		}
		return &ReturnStmt{Results: res, Line: tk.line}
	case "var":
		return p.parseVar()
	case "select", "switch", "case":
		p.errorf(tk.line, "%q was removed in go++ — use match", tk.text)
	case "go", "defer", "const", "type", "import", "continue", "goto":
		p.errorf(tk.line, "%q is not supported in v2 yet", tk.text)
	}
	lhs := p.parseExprList()
	switch p.cur().text {
	case ":=", "=", "+=", "-=", "*=", "/=":
		op := p.next().text
		return &AssignStmt{Lhs: lhs, Op: op, Rhs: p.parseExprList(), Line: tk.line}
	case "++", "--":
		op := p.next().text
		return &IncDecStmt{X: lhs[0], Op: op, Line: tk.line}
	}
	if len(lhs) > 1 {
		p.errorf(tk.line, "unexpected expression list in statement")
	}
	return &ExprStmt{X: lhs[0], Line: tk.line}
}

func (p *parser) parseVar() Stmt {
	line := p.next().line // var
	name := p.expectIdent()
	ty := p.parseType()
	var init Expr
	if p.cur().text == "=" {
		p.next()
		init = p.parseExpr(1)
	}
	return &VarStmt{Name: name, Type: ty, Init: init, Line: line}
}

func (p *parser) parseIf() Stmt {
	line := p.next().line // if
	cond := p.parseExpr(1)
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
	return &IfStmt{Cond: cond, Then: then, Else: els, Line: line}
}

func (p *parser) parseFor() Stmt {
	line := p.next().line // for
	if p.cur().text == "{" {
		return &ForStmt{Body: p.parseBlock(), Line: line}
	}
	first := p.parseExprList()
	if p.cur().text == "{" {
		return &ForStmt{Cond: first[0], Body: p.parseBlock(), Line: line}
	}
	var init Stmt
	if op := p.cur().text; op == ":=" || op == "=" {
		p.next()
		init = &AssignStmt{Lhs: first, Op: op, Rhs: p.parseExprList(), Line: line}
	} else {
		init = &ExprStmt{X: first[0], Line: line}
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
			post = &IncDecStmt{X: e[0], Op: op, Line: line}
		case "=", ":=":
			op := p.next().text
			post = &AssignStmt{Lhs: e, Op: op, Rhs: p.parseExprList(), Line: line}
		default:
			post = &ExprStmt{X: e[0], Line: line}
		}
	}
	return &ForStmt{Init: init, Cond: cond, Post: post, Body: p.parseBlock(), Line: line}
}

// ---------- match ----------

func (p *parser) parseMatch() Expr {
	line := p.next().line // match
	fair := false
	if p.cur().text == "." && p.peek().text == "fair" {
		p.next()
		p.next()
		fair = true
	}
	var subj Expr
	if p.cur().text != "{" {
		subj = p.parseExpr(1)
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
			p.errorf(line, "unterminated match")
		}
		arms = append(arms, p.parseArm())
	}
	return &MatchExpr{Subject: subj, Arms: arms, Fair: fair, Line: line}
}

func (p *parser) parseArm() MatchArm {
	line := p.cur().line
	pat := p.parsePattern()
	var guard Expr
	if p.cur().text == "if" {
		p.next()
		guard = p.parseExpr(1)
	}
	p.expect("->")
	p.skipNL()
	a := MatchArm{Pat: pat, Guard: guard, Line: line}
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
	if tk.text == "if" {
		p.next()
		return &BoolPat{X: p.parseExpr(1), Line: line}
	}
	if tk.kind == kIdent && p.peek().text == ":=" {
		bind := p.next().text
		p.next() // :=
		e := p.parseExpr(1)
		if c, ok := e.(*CallExpr); ok && len(c.Args) == 0 {
			if sel, ok := c.Fun.(*SelectorExpr); ok && sel.Sel == "recv" {
				return &RecvPat{Bind: bind, Chan: sel.X, Line: line}
			}
		}
		p.errorf(line, "recv arm must look like x := ch.recv()")
	}
	if tk.text == "_" {
		p.next()
		return &WildcardPat{Line: line}
	}
	if tk.text == "after" && p.peek().text == "(" {
		p.next()
		p.next()
		d := p.parseExpr(1)
		p.expect(")")
		return &AfterPat{D: d, Line: line}
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
				return &SendPat{Chan: sel.X, Value: c.Args[0], Line: line}
			case "closed":
				return &ClosedPat{Chan: sel.X, Line: line}
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
				return &VariantPat{Name: id.Name, Bindings: bindings, Line: line}
			}
		}
	}
	if id, ok := e.(*Ident); ok {
		// bare name: unit variant or subject binding — sema disambiguates
		return &IdentPat{Name: id.Name, Line: line}
	}
	return &LiteralPat{X: e, Line: line}
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
		x = &BinaryExpr{Op: tk.text, X: x, Y: y, Line: tk.line}
	}
	return x
}

func (p *parser) parseUnary() Expr {
	tk := p.cur()
	switch tk.text {
	case "-", "!", "<-", "+", "^":
		p.next()
		return &UnaryExpr{Op: tk.text, X: p.parseUnary(), Line: tk.line}
	}
	return p.parsePostfix()
}

func (p *parser) parsePostfix() Expr {
	x := p.parsePrimary()
	for {
		switch p.cur().text {
		case ".":
			p.next()
			x = &SelectorExpr{X: x, Sel: p.expectIdent(), Line: p.cur().line}
		case "(":
			x = &CallExpr{Fun: x, Args: p.parseCallArgs(), Line: p.cur().line}
		case "[":
			p.next()
			idx := p.parseExprList()
			p.expect("]")
			x = &IndexExpr{X: x, Index: idx, Line: p.cur().line}
		default:
			return x
		}
	}
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
		return &BasicLit{Kind: tk.kind, Value: tk.text, Line: tk.line}
	case tk.kind == kIdent:
		switch tk.text {
		case "match":
			return p.parseMatch()
		case "chan":
			line := p.next().line
			p.expect("[")
			elem := p.parseType()
			p.expect("]")
			if p.cur().text != "(" {
				p.errorf(line, "chan[T] needs (cap) in expression position")
			}
			p.next()
			var capE Expr
			if p.cur().text != ")" {
				capE = p.parseExpr(1)
			}
			p.expect(")")
			return &MakeChanExpr{Elem: elem, Cap: capE, Line: line}
		}
		p.next()
		return &Ident{Name: tk.text, Line: tk.line}
	case tk.text == "(":
		p.next()
		e := p.parseExpr(1)
		p.expect(")")
		return e
	}
	p.errorf(tk.line, "expected expression, got %q", tk.text)
	return nil
}
