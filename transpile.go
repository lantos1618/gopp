package main

import (
	"fmt"
	"strings"
)

// ---------- symbol tables ----------

type fieldInfo struct {
	name   string // binding name used in patterns and the constructor
	goName string // generated struct field name
	typ    string
}

type variantInfo struct {
	name     string
	enum     string
	tagConst string
	fields   []fieldInfo
	prelude  bool // prelude variants keep their bare names (generic funcs)
}

type enumInfo struct {
	name     string
	tagType  string
	variants []*variantInfo
}

type tr struct {
	enums     map[string]*enumInfo
	variants  map[string]*variantInfo
	chans     map[string]string // chan var name -> elem type
	vars      map[string]string // var name -> type (best-effort, for match inference)
	loops     []string
	tmpN      int
	forceExpr bool // next match must be treated as expression context
}

func newTr() *tr {
	t := &tr{
		enums:    make(map[string]*enumInfo),
		variants: make(map[string]*variantInfo),
		chans:    make(map[string]string),
		vars:     make(map[string]string),
	}
	mk := func(enumName string, vars ...*variantInfo) {
		e := &enumInfo{name: enumName, tagType: "__gopp_tag_" + enumName}
		for _, v := range vars {
			e.variants = append(e.variants, v)
			t.variants[v.name] = v
		}
		t.enums[enumName] = e
	}
	// must match the layout in prelude.go
	mk("Result",
		&variantInfo{name: "Ok", enum: "Result", tagConst: "__gopp_tag_Result_Ok",
			fields: []fieldInfo{{name: "v0", goName: "__gopp_F_Ok_0"}}, prelude: true},
		&variantInfo{name: "Err", enum: "Result", tagConst: "__gopp_tag_Result_Err",
			fields: []fieldInfo{{name: "v0", goName: "__gopp_F_Err_0"}}, prelude: true},
	)
	mk("Option",
		&variantInfo{name: "Some", enum: "Option", tagConst: "__gopp_tag_Option_Some",
			fields: []fieldInfo{{name: "v0", goName: "__gopp_F_Some_0"}}, prelude: true},
		&variantInfo{name: "None", enum: "Option", tagConst: "__gopp_tag_Option_None", prelude: true},
	)
	return t
}

// ---------- output state ----------

type xstate struct {
	toks    []token
	pos     int
	out     strings.Builder
	lastSig string // text of last emitted non-newline token
}

func isWordByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func (t *tr) emit(x *xstate, s string) {
	if s == "" {
		return
	}
	if n := x.out.Len(); n > 0 {
		prev := x.out.String()[n-1:]
		if len(prev) > 0 && isWordByte(prev[0]) && isWordByte(s[0]) {
			x.out.WriteByte(' ')
		}
	}
	x.out.WriteString(s)
	if !strings.Contains(s, "\n") {
		x.lastSig = s
	} else {
		// keep lastSig only for trailing token after last newline
		if idx := strings.LastIndex(s, "\n"); idx < len(s)-1 {
			x.lastSig = s[idx+1:]
		}
	}
}

func (t *tr) emitTok(x *xstate, tk token) { t.emit(x, tk.text) }

// peekText returns the text of the token n positions ahead (negative = behind).
func (t *tr) peekText(x *xstate, n int) string {
	i := x.pos + n
	if i < 0 || i >= len(x.toks) {
		return ""
	}
	return x.toks[i].text
}

func (t *tr) skipNL(x *xstate) {
	for x.pos < len(x.toks) && x.toks[x.pos].kind == kNewline {
		x.pos++
	}
}

func joinToks(toks []token) string {
	var b strings.Builder
	last := byte(0)
	for _, tk := range toks {
		s := tk.text
		if tk.kind == kNewline {
			s = " "
		}
		if b.Len() > 0 && isWordByte(last) && len(s) > 0 && isWordByte(s[0]) {
			b.WriteByte(' ')
		}
		b.WriteString(s)
		if len(s) > 0 {
			last = s[len(s)-1]
		}
	}
	return b.String()
}

// ---------- main pass ----------

func (t *tr) xform(toks []token) (string, error) {
	x := &xstate{toks: toks}
	for x.pos < len(x.toks) && x.toks[x.pos].kind != kEOF {
		if err := t.step(x); err != nil {
			return "", err
		}
	}
	return x.out.String(), nil
}

func (t *tr) step(x *xstate) error {
	tok := x.toks[x.pos]
	if tok.kind == kNewline {
		t.emit(x, "\n")
		x.pos++
		return nil
	}
	switch {
	case tok.text == "enum" && t.peekText(x, 1) != "" && x.toks[x.pos+1].kind == kIdent:
		return t.parseEnum(x)
	case tok.text == "match":
		return t.parseMatch(x)
	case tok.text == "loop" && t.peekText(x, 1) == "{":
		return t.parseLoop(x)
	case tok.text == "break" && t.peekText(x, 1) == "loop":
		if len(t.loops) == 0 {
			return fmt.Errorf("line %d: 'break loop' outside of a loop block", tok.line)
		}
		t.emit(x, "break "+t.loops[len(t.loops)-1])
		x.pos += 2
		return nil
	case tok.text == "chan" && t.peekText(x, 1) == "[":
		return t.parseChan(x)
	case tok.text == "var" && x.pos+3 < len(x.toks) && x.toks[x.pos+1].kind == kIdent &&
		t.peekText(x, 2) == "map" && t.peekText(x, 3) == "[":
		return t.parseVarMap(x)
	case tok.text == "fn":
		t.emit(x, "func")
		x.pos++
		return nil
	case tok.text == "?":
		return fmt.Errorf("line %d: '?' try operator not supported in gopp v0.1; match on the Result instead", tok.line)
	case tok.kind == kIdent && t.peekText(x, 1) == "." && t.peekText(x, 3) == "(" &&
		(t.peekText(x, 2) == "send" || t.peekText(x, 2) == "recv" ||
			t.peekText(x, 2) == "close" || t.peekText(x, 2) == "closed"):
		return t.parseChanMethod(x)
	case tok.kind == kIdent:
		if v, ok := t.variants[tok.text]; ok && !v.prelude {
			next := t.peekText(x, 1)
			if next == "(" {
				// constructor call: Failed("boom") -> Status_Failed("boom")
				t.emit(x, v.enum+"_"+v.name)
				x.pos++
				return nil
			}
			if len(v.fields) == 0 {
				// bare unit variant value: Active -> Status_Active()
				t.emit(x, v.enum+"_"+v.name+"()")
				x.pos++
				return nil
			}
		}
		t.emitTok(x, tok)
		x.pos++
		return nil
	default:
		t.emitTok(x, tok)
		x.pos++
		return nil
	}
}

// processUntil processes tokens through the normal step loop until a token
// with text closer appears at bracket depth 0 (not consumed).
func (t *tr) processUntil(x *xstate, closer string) error {
	depth := 0
	for x.pos < len(x.toks) && x.toks[x.pos].kind != kEOF {
		tk := x.toks[x.pos]
		if tk.text == closer && depth == 0 {
			return nil
		}
		switch tk.text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			depth--
		}
		if err := t.step(x); err != nil {
			return err
		}
	}
	return fmt.Errorf("unexpected end of input, expected %s", closer)
}

// captureBlock collects tokens until the matching closing brace of an
// already-consumed opening brace. Consumes the final "}".
func captureBlock(x *xstate) ([]token, error) {
	depth := 1
	var out []token
	for x.pos < len(x.toks) {
		tk := x.toks[x.pos]
		x.pos++
		switch tk.text {
		case "{":
			depth++
		case "}":
			depth--
			if depth == 0 {
				return out, nil
			}
		}
		out = append(out, tk)
	}
	return nil, fmt.Errorf("unterminated block")
}

// ---------- enum ----------

func (t *tr) parseEnum(x *xstate) error {
	line := x.toks[x.pos].line
	x.pos++ // enum
	name := x.toks[x.pos].text
	x.pos++
	if t.peekText(x, 0) == "[" {
		return fmt.Errorf("line %d: generic enums not supported in gopp v0.1 (use Result/Option from the prelude)", line)
	}
	t.skipNL(x)
	if t.peekText(x, 0) != "{" {
		return fmt.Errorf("line %d: expected '{' after enum %s", line, name)
	}
	x.pos++
	e := &enumInfo{name: name, tagType: "__gopp_tag_" + name}
	for {
		t.skipNL(x)
		if t.peekText(x, 0) == "}" {
			x.pos++
			break
		}
		if x.pos >= len(x.toks) || x.toks[x.pos].kind != kIdent {
			return fmt.Errorf("line %d: expected variant name in enum %s", line, name)
		}
		v := &variantInfo{name: x.toks[x.pos].text, enum: name, tagConst: e.tagType + "_" + x.toks[x.pos].text}
		x.pos++
		if t.peekText(x, 0) == "(" {
			x.pos++
			i := 0
			for {
				t.skipNL(x)
				if t.peekText(x, 0) == ")" {
					x.pos++
					break
				}
				var ft []token
				depth := 0
				for x.pos < len(x.toks) {
					tk := x.toks[x.pos]
					if (tk.text == "," || tk.text == ")") && depth == 0 {
						break
					}
					switch tk.text {
					case "(", "[", "{":
						depth++
					case ")", "]", "}":
						depth--
					}
					ft = append(ft, tk)
					x.pos++
				}
				words := strings.Fields(joinToks(ft))
				fname, ftype := fmt.Sprintf("v%d", i), joinToks(ft)
				if len(words) == 2 && words[0] != words[1] && isWordByte(words[0][0]) {
					fname, ftype = words[0], words[1]
				}
				v.fields = append(v.fields, fieldInfo{
					name:   fname,
					goName: "__gopp_F_" + v.name + "_" + fname,
					typ:    ftype,
				})
				i++
				if t.peekText(x, 0) == "," {
					x.pos++
					continue
				}
				if t.peekText(x, 0) == ")" {
					x.pos++
					break
				}
				return fmt.Errorf("line %d: expected ',' or ')' in variant %s", line, v.name)
			}
		}
		e.variants = append(e.variants, v)
		t.variants[v.name] = v
		// optional separators between variants
		if t.peekText(x, 0) == ";" || t.peekText(x, 0) == "," {
			x.pos++
		}
	}
	t.enums[name] = e
	t.emitEnum(x, e)
	return nil
}

func (t *tr) emitEnum(x *xstate, e *enumInfo) {
	t.emit(x, "type "+e.tagType+" int\n")
	t.emit(x, "const (\n")
	for i, v := range e.variants {
		if i == 0 {
			t.emit(x, v.tagConst+" "+e.tagType+" = iota\n")
		} else {
			t.emit(x, v.tagConst+"\n")
		}
	}
	t.emit(x, ")\n")
	t.emit(x, "type "+e.name+" struct {\n")
	t.emit(x, "__gopp_tag "+e.tagType+"\n")
	for _, v := range e.variants {
		for _, f := range v.fields {
			t.emit(x, f.goName+" "+f.typ+"\n")
		}
	}
	t.emit(x, "}\n")
	for _, v := range e.variants {
		var params []string
		for _, f := range v.fields {
			params = append(params, f.name+" "+f.typ)
		}
		t.emit(x, "func "+e.name+"_"+v.name+"("+strings.Join(params, ", ")+") "+e.name+" {\n")
		t.emit(x, "var __gopp_z "+e.name+"\n")
		t.emit(x, "__gopp_z.__gopp_tag = "+v.tagConst+"\n")
		for _, f := range v.fields {
			t.emit(x, "__gopp_z."+f.goName+" = "+f.name+"\n")
		}
		t.emit(x, "return __gopp_z\n}\n")
	}
}

// ---------- channels ----------

func (t *tr) parseChan(x *xstate) error {
	chanIdx := x.pos // index of the `chan` token
	x.pos += 2       // consume chan [
	var ty []token
	depth := 0
	for x.pos < len(x.toks) {
		tk := x.toks[x.pos]
		if tk.text == "]" && depth == 0 {
			break
		}
		switch tk.text {
		case "[", "(", "{":
			depth++
		case "]", ")", "}":
			depth--
		}
		ty = append(ty, tk)
		x.pos++
	}
	if x.pos >= len(x.toks) {
		return fmt.Errorf("unterminated chan[...]")
	}
	tyText := joinToks(ty)
	x.pos++ // consume ]
	// register `ident := chan[T]...` so recv-arm bodies can be typed
	if chanIdx >= 2 && x.toks[chanIdx-1].text == ":=" && x.toks[chanIdx-2].kind == kIdent {
		t.chans[x.toks[chanIdx-2].text] = tyText
	}
	if t.peekText(x, 0) == "(" {
		x.pos++
		t.emit(x, "make(chan "+tyText)
		if t.peekText(x, 0) != ")" {
			t.emit(x, ", ")
			if err := t.processUntil(x, ")"); err != nil {
				return err
			}
		}
		x.pos++ // consume )
		t.emit(x, ")")
	} else {
		// type position: chan[Job] -> chan Job
		t.emit(x, "chan "+tyText)
	}
	return nil
}

// parseVarMap rewrites `var m map[K]V` (no initializer) into
// `var m map[K]V = make(map[K]V)` so go++ maps are usable the moment they
// are declared — no nil-map panic on first write. Declarations with an
// explicit initializer pass through unchanged. Maps declared inside
// `var ( ... )` blocks or as struct fields are not yet rewritten.
func (t *tr) parseVarMap(x *xstate) error {
	start := x.pos // at `var`; x.toks[start+1] is the name
	x.pos += 3     // consume var name map
	// consume the key type [K]
	depth := 0
	for x.pos < len(x.toks) {
		tk := x.toks[x.pos]
		x.pos++
		if tk.text == "[" {
			depth++
		}
		if tk.text == "]" {
			depth--
			if depth == 0 {
				break
			}
		}
	}
	// consume the value type: everything up to newline or top-level '='
	depth = 0
	for x.pos < len(x.toks) {
		tk := x.toks[x.pos]
		if tk.kind == kNewline || tk.kind == kEOF {
			break
		}
		if tk.text == "=" && depth == 0 {
			break
		}
		switch tk.text {
		case "[", "(", "{":
			depth++
		case "]", ")", "}":
			depth--
		}
		x.pos++
	}
	mapType := joinToks(x.toks[start+2 : x.pos])
	t.emit(x, "var")
	t.emitTok(x, x.toks[start+1])
	if t.peekText(x, 0) == "=" {
		// explicit initializer: pass through, main loop resumes at '='
		t.emit(x, mapType)
		return nil
	}
	t.emit(x, mapType+" = make("+mapType+")")
	return nil
}

func (t *tr) parseChanMethod(x *xstate) error {
	name := x.toks[x.pos]
	method := t.peekText(x, 2)
	switch method {
	case "recv":
		if t.peekText(x, 4) != ")" {
			return fmt.Errorf("line %d: recv() takes no arguments", name.line)
		}
		t.emit(x, "<-")
		t.emitTok(x, name)
		x.pos += 5
	case "close":
		if t.peekText(x, 4) != ")" {
			return fmt.Errorf("line %d: close() takes no arguments", name.line)
		}
		t.emit(x, "close")
		t.emit(x, "(")
		t.emitTok(x, name)
		t.emit(x, ")")
		x.pos += 5
	case "closed":
		return fmt.Errorf("line %d: .closed() is only meaningful as a match arm and is not supported in gopp v0.1", name.line)
	case "send":
		t.emitTok(x, name)
		t.emit(x, "<-")
		x.pos += 4 // consume name . send (
		if err := t.processUntil(x, ")"); err != nil {
			return err
		}
		x.pos++ // consume )
	}
	return nil
}

// ---------- loop ----------

func (t *tr) parseLoop(x *xstate) error {
	x.pos += 2 // consume loop {
	body, err := captureBlock(x)
	if err != nil {
		return err
	}
	label := fmt.Sprintf("__gopp_loop%d", t.tmpN)
	t.tmpN++
	t.loops = append(t.loops, label)
	inner, err := t.xform(body)
	t.loops = t.loops[:len(t.loops)-1]
	if err != nil {
		return err
	}
	t.emit(x, label+": for {\n"+inner+"}")
	return nil
}

// ---------- match ----------

type armKind int

const (
	armVariant armKind = iota
	armLiteral
	armWildcard
	armBind
	armRecv
	armSend
	armAfter
	armBool
)

type arm struct {
	kind      armKind
	pat       []token // literal pattern
	variant   *variantInfo
	bindings  []string // payload bindings (variant) or 1 elem (recv)
	chanExpr  []token
	sendArgs  []token
	after     []token
	guard     []token
	cond      []token // bool arm
	body      []token
	bodyBlock bool
	line      int
}

var exprPrevs = map[string]bool{":=": true, "=": true, "return": true, "(": true, ",": true, "[": true, "->": true}

func (t *tr) parseMatch(x *xstate) error {
	line := x.toks[x.pos].line
	exprCtx := t.forceExpr || exprPrevs[x.lastSig]
	t.forceExpr = false
	x.pos++ // match
	t.skipNL(x)
	if t.peekText(x, 0) == "." && t.peekText(x, 1) == "fair" {
		// .fair: accepted; Go's select already picks randomly.
		x.pos += 2
		t.skipNL(x)
	}
	var subj []token
	if t.peekText(x, 0) != "{" {
		depth := 0
		for x.pos < len(x.toks) {
			tk := x.toks[x.pos]
			if tk.text == "{" && depth == 0 {
				break
			}
			switch tk.text {
			case "(", "[":
				depth++
			case ")", "]":
				depth--
			}
			subj = append(subj, tk)
			x.pos++
		}
	}
	if t.peekText(x, 0) != "{" {
		return fmt.Errorf("line %d: expected '{' in match", line)
	}
	x.pos++ // {
	var arms []*arm
	for {
		t.skipNL(x)
		if t.peekText(x, 0) == "}" {
			x.pos++
			break
		}
		if x.pos >= len(x.toks) || x.toks[x.pos].kind == kEOF {
			return fmt.Errorf("line %d: unterminated match block", line)
		}
		a, err := t.parseArm(x)
		if err != nil {
			return err
		}
		arms = append(arms, a)
		t.skipNL(x)
	}
	if len(arms) == 0 {
		return fmt.Errorf("line %d: match with no arms", line)
	}
	for i, a := range arms {
		if a.kind == armWildcard && len(a.guard) == 0 && i != len(arms)-1 {
			return fmt.Errorf("line %d: wildcard arm must be the last arm", a.line)
		}
	}
	// classify
	hasChan := false
	for _, a := range arms {
		if a.kind == armRecv || a.kind == armSend || a.kind == armAfter {
			hasChan = true
		}
	}
	if hasChan {
		for _, a := range arms {
			switch a.kind {
			case armRecv, armSend, armAfter, armWildcard:
			default:
				return fmt.Errorf("line %d: cannot mix channel arms with value/enum arms in gopp v0.1", a.line)
			}
		}
		return t.emitSelect(x, arms, exprCtx)
	}
	if len(subj) == 0 {
		for _, a := range arms {
			if a.kind != armBool && a.kind != armWildcard {
				return fmt.Errorf("line %d: subject-less match arms must be channel patterns, 'if' conditions, or '_'", a.line)
			}
		}
		return t.emitChain(x, nil, arms, exprCtx)
	}
	for _, a := range arms {
		if a.kind == armBool {
			return fmt.Errorf("line %d: 'if' boolean arms need a subject-less match", a.line)
		}
	}
	return t.emitChain(x, subj, arms, exprCtx)
}

func (t *tr) parseArm(x *xstate) (*arm, error) {
	a := &arm{line: x.toks[x.pos].line}
	// pattern region up to top-level ->
	var pat []token
	depth := 0
	for x.pos < len(x.toks) {
		tk := x.toks[x.pos]
		if tk.text == "->" && depth == 0 {
			x.pos++
			break
		}
		if tk.kind == kNewline && depth == 0 {
			return nil, fmt.Errorf("line %d: expected -> in match arm", a.line)
		}
		switch tk.text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			depth--
		}
		pat = append(pat, tk)
		x.pos++
	}
	if len(pat) == 0 {
		return nil, fmt.Errorf("line %d: empty match arm pattern", a.line)
	}
	// body
	t.skipNL(x)
	if t.peekText(x, 0) == "{" {
		x.pos++
		blk, err := captureBlock(x)
		if err != nil {
			return nil, err
		}
		a.body = blk
		a.bodyBlock = true
	} else {
		depth := 0
		for x.pos < len(x.toks) {
			tk := x.toks[x.pos]
			if (tk.kind == kNewline || tk.kind == kEOF) && depth == 0 {
				break
			}
			switch tk.text {
			case "(", "[", "{":
				depth++
			case ")", "]", "}":
				depth--
			}
			a.body = append(a.body, tk)
			x.pos++
		}
	}
	// classify pattern
	if len(pat) == 1 && pat[0].text == "_" {
		a.kind = armWildcard
		return a, nil
	}
	if pat[0].text == "if" {
		a.kind = armBool
		a.cond = pat[1:]
		return a, nil
	}
	// guard split: top-level `if`
	guardAt := -1
	depth = 0
	for i, tk := range pat {
		switch tk.text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			depth--
		}
		if tk.text == "if" && depth == 0 {
			guardAt = i
			break
		}
	}
	if guardAt >= 0 {
		a.guard = pat[guardAt+1:]
		pat = pat[:guardAt]
		if len(pat) == 0 {
			return nil, fmt.Errorf("line %d: empty pattern before guard", a.line)
		}
	}
	// after(...)
	if pat[0].text == "after" && len(pat) >= 3 && pat[1].text == "(" && pat[len(pat)-1].text == ")" {
		if len(a.guard) > 0 {
			return nil, fmt.Errorf("line %d: guards on after() arms not supported in gopp v0.1", a.line)
		}
		a.kind = armAfter
		a.after = pat[2 : len(pat)-1]
		return a, nil
	}
	// recv: binding := expr .recv()
	if len(pat) >= 5 && pat[1].text == ":=" {
		rhs := pat[2:]
		n := len(rhs)
		if n >= 4 && rhs[n-4].text == "." && rhs[n-3].text == "recv" && rhs[n-2].text == "(" && rhs[n-1].text == ")" {
			if len(a.guard) > 0 {
				return nil, fmt.Errorf("line %d: guards on recv arms not supported in gopp v0.1", a.line)
			}
			a.kind = armRecv
			a.bindings = []string{pat[0].text}
			a.chanExpr = rhs[:n-4]
			return a, nil
		}
	}
	// send: expr .send(args)
	{
		n := len(pat)
		if n >= 5 && pat[n-1].text == ")" {
			for i := 0; i+3 < n; i++ {
				if pat[i].text == "." && pat[i+1].text == "send" && pat[i+2].text == "(" {
					if len(a.guard) > 0 {
						return nil, fmt.Errorf("line %d: guards on send arms not supported in gopp v0.1", a.line)
					}
					a.kind = armSend
					a.chanExpr = pat[:i]
					a.sendArgs = pat[i+3 : n-1]
					return a, nil
				}
			}
		}
	}
	// closed: rejected explicitly
	{
		n := len(pat)
		if n >= 4 && pat[n-4].text == "." && pat[n-3].text == "closed" {
			return nil, fmt.Errorf("line %d: .closed() match arms not supported in gopp v0.1", a.line)
		}
	}
	// variant pattern
	if v, ok := t.variants[pat[0].text]; ok {
		a.kind = armVariant
		a.variant = v
		if len(pat) > 1 {
			if pat[1].text != "(" || pat[len(pat)-1].text != ")" {
				return nil, fmt.Errorf("line %d: malformed variant pattern", a.line)
			}
			for _, tk := range pat[2 : len(pat)-1] {
				if tk.text == "," || tk.kind == kNewline {
					continue
				}
				if tk.kind != kIdent {
					return nil, fmt.Errorf("line %d: variant bindings must be identifiers", a.line)
				}
				a.bindings = append(a.bindings, tk.text)
			}
			if len(a.bindings) != len(v.fields) {
				return nil, fmt.Errorf("line %d: %s has %d field(s), pattern binds %d", a.line, v.name, len(v.fields), len(a.bindings))
			}
		} else if len(v.fields) > 0 {
			return nil, fmt.Errorf("line %d: %s carries data; use %s(%s) in the pattern", a.line, v.name, v.name, v.fields[0].name)
		}
		return a, nil
	}
	// bare identifier: binds the subject value (v if v > 100 -> ...)
	if len(pat) == 1 && pat[0].kind == kIdent {
		a.kind = armBind
		a.bindings = []string{pat[0].text}
		return a, nil
	}
	// literal / expression pattern
	a.kind = armLiteral
	a.pat = pat
	return a, nil
}

func (t *tr) classifyBody(body []token) (string, error) {
	if len(body) == 1 {
		tk := body[0]
		switch tk.kind {
		case kString:
			return "string", nil
		case kInt:
			return "int", nil
		case kFloat:
			return "float64", nil
		case kRune:
			return "rune", nil
		case kIdent:
			if tk.text == "true" || tk.text == "false" {
				return "bool", nil
			}
			if v, ok := t.variants[tk.text]; ok && !v.prelude {
				return v.enum, nil
			}
			if ty, ok := t.vars[tk.text]; ok {
				return ty, nil
			}
		}
		return "", fmt.Errorf("cannot infer type of %q", tk.text)
	}
	for _, tk := range body {
		if tk.kind == kString {
			return "string", nil
		}
	}
	// pure numeric expression: literals, arithmetic ops, parens (e.g. -1, 1+2)
	num, arith := true, false
	for _, tk := range body {
		switch {
		case tk.kind == kInt:
			arith = true
		case tk.kind == kFloat:
			return "float64", nil
		case tk.kind == kOp && strings.ContainsAny(tk.text, "+-*/%()"):
		default:
			num = false
		}
	}
	if num && arith {
		return "int", nil
	}
	return "", fmt.Errorf("cannot infer match result type from %q; simplify arm bodies", joinToks(body))
}

func (t *tr) inferArms(arms []*arm) (string, error) {
	ty := ""
	for _, a := range arms {
		if a.bodyBlock {
			return "", fmt.Errorf("line %d: cannot infer result type of a block body; use single-expression arms", a.line)
		}
		bt, err := t.classifyBody(a.body)
		if err != nil {
			return "", fmt.Errorf("line %d: %v", a.line, err)
		}
		if ty == "" {
			ty = bt
		} else if ty != bt {
			return "", fmt.Errorf("line %d: match arms produce different types (%s vs %s)", a.line, ty, bt)
		}
	}
	return ty, nil
}

// emitSelect emits a subject-less match over channel arms as a Go select.
func (t *tr) emitSelect(x *xstate, arms []*arm, exprCtx bool) error {
	// pre-register recv bindings for body type inference
	for _, a := range arms {
		if a.kind == armRecv && a.bindings[0] != "_" {
			chText := joinToks(a.chanExpr)
			elem, ok := t.chans[chText]
			if !ok {
				return fmt.Errorf("line %d: unknown channel %s (declare it with ch := chan[T](cap))", a.line, chText)
			}
			t.vars[a.bindings[0]] = elem
		}
	}
	T := ""
	var err error
	if exprCtx {
		T, err = t.inferArms(arms)
		if err != nil {
			return err
		}
	}
	var b strings.Builder
	if exprCtx {
		b.WriteString("func() " + T + " { ")
	}
	b.WriteString("select {\n")
	for _, a := range arms {
		var head string
		switch a.kind {
		case armRecv:
			chText := joinToks(a.chanExpr)
			if a.bindings[0] == "_" {
				head = "case <-" + chText + ":"
			} else {
				head = "case " + a.bindings[0] + " := <-" + chText + ":"
			}
		case armSend:
			args, err := t.xform(a.sendArgs)
			if err != nil {
				return err
			}
			head = "case " + joinToks(a.chanExpr) + " <- " + args + ":"
		case armAfter:
			d, err := t.xform(a.after)
			if err != nil {
				return err
			}
			head = "case <-goppAfter(" + d + "):"
		case armWildcard:
			head = "default:"
		}
		b.WriteString(head + "\n")
		if err := t.emitArmBody(&b, a, exprCtx); err != nil {
			return err
		}
	}
	b.WriteString("}")
	if exprCtx {
		b.WriteString("\n}()")
	}
	t.emit(x, b.String())
	return nil
}

// emitChain emits a subject match (or subject-less boolean match) as an
// if/else chain, optionally wrapped in an IIFE for expression context.
func (t *tr) emitChain(x *xstate, subj []token, arms []*arm, exprCtx bool) error {
	m := fmt.Sprintf("__gopp_m%d", t.tmpN)
	t.tmpN++
	subjText := ""
	var err error
	if len(subj) > 0 {
		subjText, err = t.xform(subj)
		if err != nil {
			return err
		}
	}
	// Hoist all bindings ahead of the if-chain (guards may reference them),
	// renaming to unique temps so same-named bindings in different arms
	// cannot collide. Uses in guard/body tokens are renamed to match.
	var pre strings.Builder
	if len(subj) > 0 {
		pre.WriteString(m + " := " + subjText + "\n")
	}
	for _, a := range arms {
		switch a.kind {
		case armBind:
			old := a.bindings[0]
			if old == "_" {
				continue
			}
			uniq := fmt.Sprintf("__gopp_b%d", t.tmpN)
			t.tmpN++
			pre.WriteString(uniq + " := " + m + "\n")
			renameIdents(a.guard, old, uniq)
			renameIdents(a.body, old, uniq)
			a.bindings[0] = uniq
		case armVariant:
			for k, bd := range a.bindings {
				if bd == "_" || k >= len(a.variant.fields) {
					continue
				}
				uniq := fmt.Sprintf("__gopp_b%d", t.tmpN)
				t.tmpN++
				pre.WriteString(uniq + " := " + m + "." + a.variant.fields[k].goName + "\n")
				renameIdents(a.guard, bd, uniq)
				renameIdents(a.body, bd, uniq)
				a.bindings[k] = uniq
				t.vars[uniq] = a.variant.fields[k].typ
			}
		}
	}
	T := ""
	if exprCtx {
		T, err = t.inferArms(arms)
		if err != nil {
			return err
		}
	}
	var b strings.Builder
	if exprCtx {
		b.WriteString("func() " + T + " { ")
	} else {
		b.WriteString("{ ")
	}
	b.WriteString(pre.String())
	for i, a := range arms {
		var cond string
		switch a.kind {
		case armWildcard:
			if len(a.guard) == 0 {
				cond = ""
			} else {
				g, err := t.xform(a.guard)
				if err != nil {
					return err
				}
				cond = "(" + g + ")"
			}
		case armVariant:
			cond = m + ".__gopp_tag == " + a.variant.tagConst
		case armBind:
			cond = "true"
		case armLiteral:
			p, err := t.xform(a.pat)
			if err != nil {
				return err
			}
			cond = m + " == (" + p + ")"
		case armBool:
			c, err := t.xform(a.cond)
			if err != nil {
				return err
			}
			cond = "(" + c + ")"
		}
		if len(a.guard) > 0 && a.kind != armWildcard {
			g, err := t.xform(a.guard)
			if err != nil {
				return err
			}
			cond = "(" + cond + ") && (" + g + ")"
		}
		var kw string
		switch {
		case a.kind == armWildcard && len(a.guard) == 0 && i == 0:
			kw = "if true"
		case a.kind == armWildcard && len(a.guard) == 0:
			kw = "else"
		case i == 0:
			kw = "if " + cond
		default:
			kw = "else if " + cond
		}
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(kw + " {\n")
		if err := t.emitArmBody(&b, a, exprCtx); err != nil {
			return err
		}
		b.WriteString("}")
	}
	// non-exhaustive guard
	last := arms[len(arms)-1]
	if !(last.kind == armWildcard && len(last.guard) == 0) {
		b.WriteString(" else { panic(\"gopp: non-exhaustive match\") }")
	}
	b.WriteString("\n}")
	if exprCtx {
		b.WriteString("()")
	}
	t.emit(x, b.String())
	return nil
}

// renameIdents replaces identifier occurrences in a token stream, skipping
// selector positions (x.name) so field/method access is left alone.
func renameIdents(toks []token, from, to string) {
	for i := range toks {
		if toks[i].kind == kIdent && toks[i].text == from {
			if i > 0 && toks[i-1].text == "." {
				continue
			}
			toks[i].text = to
		}
	}
}

func (t *tr) emitArmBody(b *strings.Builder, a *arm, exprCtx bool) error {
	if a.bodyBlock {
		body, err := t.xform(a.body)
		if err != nil {
			return err
		}
		b.WriteString("{\n" + body + "}\n")
		return nil
	}
	if len(a.body) > 0 && a.body[0].text == "match" {
		t.forceExpr = true
	}
	body, err := t.xform(a.body)
	if err != nil {
		return err
	}
	if exprCtx {
		b.WriteString("return " + body + "\n")
	} else {
		b.WriteString(body + "\n")
	}
	return nil
}
