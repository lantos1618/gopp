package main

// lsp.go — go++ language server v2 (§28): LSP over stdio, stdlib only.
// One goroutine, sequential message processing; each didOpen/didChange
// re-runs the real pipeline (lex -> parse -> checkImports) on the
// in-memory text and publishes the full diagnostic set. Buffers with
// imports are checked import-aware: dependency packages are loaded from
// the buffer's on-disk directory (current even when the buffer isn't
// saved) and their checkers wired in by qualifier; anything missing
// degrades to single-file mode (the qualifier diagnoses as undefined).
//
// Position logic is line-based where the AST still lacks columns;
// identAt/hover/definition refine by column where nodes carry Col.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ---------- protocol types ----------

// lspID is a JSON-RPC id: number or string, echoed back verbatim.
type lspID = json.RawMessage

type lspRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      lspID           `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type lspResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      lspID     `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *lspError `json:"error,omitempty"`
}

// MarshalJSON keeps JSON-RPC honest: a null result must still be present
// (`"result": null`), and result/error never appear together.
func (r *lspResponse) MarshalJSON() ([]byte, error) {
	m := map[string]any{"jsonrpc": r.JSONRPC, "id": r.ID}
	if r.Error != nil {
		m["error"] = r.Error
	} else {
		m["result"] = r.Result
	}
	return json.Marshal(m)
}

type lspNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type lspError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// LSP positions are 0-based; the compiler's are 1-based.
type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type lspLocation struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

type lspDiagnostic struct {
	Range    lspRange `json:"range"`
	Severity int      `json:"severity"` // 1 error, 2 warning
	Source   string   `json:"source"`
	Message  string   `json:"message"`
}

type textDocumentItem struct {
	URI  string `json:"uri"`
	Text string `json:"text"`
}

type docParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

type positionParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
	Position     lspPosition      `json:"position"`
}

type hoverResult struct {
	Contents markupContent `json:"contents"`
}

type markupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type completionItem struct {
	Label  string `json:"label"`
	Kind   int    `json:"kind,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// CompletionItemKind: 3 Function, 7 Class, 13 Enum, 14 Keyword,
// 20 EnumMember, 6 Variable.
const (
	kindFunction   = 3
	kindClass      = 7
	kindEnum       = 13
	kindKeyword    = 14
	kindEnumMember = 20
	kindVariable   = 6
)

// SymbolKind: 10 Enum, 12 Function, 23 Struct.
const (
	symEnum     = 10
	symFunction = 12
	symStruct   = 23
)

type documentSymbol struct {
	Name           string   `json:"name"`
	Kind           int      `json:"kind"`
	Range          lspRange `json:"range"`
	SelectionRange lspRange `json:"selectionRange"`
}

// ---------- server ----------

// lspDoc is one open buffer plus the latest analysis side tables.
type lspDoc struct {
	text  string
	lines []string
	f     *File
	chk   *checker
}

type lspServer struct {
	in      *bufio.Reader
	out     io.Writer
	docs    map[string]*lspDoc
	gotInit bool
	gotExit bool
}

// serveLSP runs the language server loop on the given streams until the
// client sends exit, closes the stream, or a malformed frame arrives
// (the latter is returned as an error).
func serveLSP(stdin io.Reader, stdout io.Writer) error {
	s := &lspServer{in: bufio.NewReader(stdin), out: stdout, docs: map[string]*lspDoc{}}
	for !s.gotExit {
		body, err := readFrame(s.in)
		if err == io.EOF {
			return nil // client went away
		}
		if err != nil {
			return err
		}
		var req lspRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return fmt.Errorf("lsp: invalid JSON-RPC message: %w", err)
		}
		s.dispatch(&req)
	}
	return nil
}

// readFrame reads one `Content-Length: N\r\n\r\n<body>` message.
func readFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF && line == "" && length < 0 {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("lsp: truncated header: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if length < 0 {
				return nil, fmt.Errorf("lsp: header without Content-Length")
			}
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("lsp: malformed header line %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("lsp: bad Content-Length %q", value)
			}
			length = n
		}
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("lsp: truncated body: %w", err)
	}
	return body, nil
}

func (s *lspServer) write(v any) {
	body, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(s.out, "Content-Length: %d\r\n\r\n", len(body))
	s.out.Write(body)
}

func (s *lspServer) reply(id lspID, result any) {
	s.write(&lspResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *lspServer) replyErr(id lspID, code int, msg string) {
	s.write(&lspResponse{JSONRPC: "2.0", ID: id, Error: &lspError{Code: code, Message: msg}})
}

func (s *lspServer) notify(method string, params any) {
	s.write(&lspNotification{JSONRPC: "2.0", Method: method, Params: params})
}

// ---------- dispatch ----------

func (s *lspServer) dispatch(req *lspRequest) {
	isRequest := req.ID != nil
	switch req.Method {
	case "initialize":
		s.gotInit = true
		s.reply(req.ID, map[string]any{
			"capabilities": map[string]any{
				"textDocumentSync":       1, // full
				"hoverProvider":          true,
				"definitionProvider":     true,
				"completionProvider":     map[string]any{},
				"documentSymbolProvider": true,
			},
			"serverInfo": map[string]any{"name": "gopp-lsp", "version": "v1"},
		})
	case "initialized":
		// nothing to do
	case "shutdown":
		s.reply(req.ID, nil)
	case "exit":
		s.gotExit = true
	case "textDocument/didOpen":
		var p docParams
		if json.Unmarshal(req.Params, &p) == nil {
			s.openDoc(p.TextDocument.URI, p.TextDocument.Text)
		}
	case "textDocument/didChange":
		var p docParams
		if json.Unmarshal(req.Params, &p) != nil {
			return
		}
		// textDocumentSync is full: take the last content change.
		var changes []struct {
			Text string `json:"text"`
		}
		var raw struct {
			ContentChanges json.RawMessage `json:"contentChanges"`
		}
		if json.Unmarshal(req.Params, &raw) == nil &&
			json.Unmarshal(raw.ContentChanges, &changes) == nil && len(changes) > 0 {
			s.openDoc(p.TextDocument.URI, changes[len(changes)-1].Text)
		}
	case "textDocument/didClose":
		var p docParams
		if json.Unmarshal(req.Params, &p) == nil {
			delete(s.docs, p.TextDocument.URI)
			s.publishDiags(p.TextDocument.URI, nil) // clear the client's markers
		}
	case "textDocument/hover":
		if isRequest {
			s.onHover(req)
		}
	case "textDocument/definition":
		if isRequest {
			s.onDefinition(req)
		}
	case "textDocument/completion":
		if isRequest {
			s.onCompletion(req)
		}
	case "textDocument/documentSymbol":
		if isRequest {
			s.onDocumentSymbol(req)
		}
	default:
		if isRequest {
			s.replyErr(req.ID, codeMethodNotFound, "unknown method: "+req.Method)
		}
		// unknown notifications are ignored (LSP spec)
	}
}

// doc returns the open document for uri, or nil (unknown/unopened URIs
// are not an error — requests on them answer null/empty).
func (s *lspServer) doc(uri string) *lspDoc { return s.docs[uri] }

// ---------- analysis ----------

// openDoc (re)analyzes text and publishes the resulting diagnostics.
// The pipeline is the compiler's own: lex -> parse -> checkImports with
// the buffer's imports wired in (depCheckers). Like the CLI, sema is
// skipped when syntax errors exist; stale check tables are dropped too
// so hover/definition never answer from an out-of-date AST.
func (s *lspServer) openDoc(uri, text string) {
	d := &lspDoc{text: text, lines: strings.Split(text, "\n")}
	diags := &Diagnostics{}
	toks, err := lex(text)
	if err != nil {
		diagFromError(diags, err)
	} else {
		f, pdiags := parse(toks)
		diags.items = append(diags.items, pdiags.items...)
		d.f = f
		if !diags.HasErrors() {
			chk, cdiags := checkImports(f, depCheckers(uri, f), nil, checkOpts{src: text})
			diags.items = append(diags.items, cdiags.items...)
			d.chk = chk
		}
	}
	s.docs[uri] = d
	s.publishDiags(uri, diags.sorted())
}

// depCheckers loads the buffer's imports from its on-disk directory and
// returns qualifier -> checked dependency, exactly like checkGraph wires
// them (dep.name). Deps are read from disk, so they're current even when
// the edited buffer isn't saved; any failure (no file URI, missing
// directory, broken dep) simply omits that import, degrading to
// single-file mode where the qualifier diagnoses as undefined.
func depCheckers(uri string, f *File) map[string]*checker {
	imports := map[string]*checker{}
	if len(f.Imports) == 0 {
		return imports
	}
	path, ok := strings.CutPrefix(uri, "file://")
	if !ok || path == "" {
		return imports
	}
	dir := filepath.Dir(path)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return imports
	}
	for _, imp := range f.Imports {
		dep := loadGraph(filepath.Join(dir, filepath.FromSlash(imp.Path)))
		checkGraph(dep)
		if dep.chk != nil && dep.name != "" {
			imports[dep.name] = dep.chk
		}
	}
	return imports
}

func (s *lspServer) publishDiags(uri string, ds []Diagnostic) {
	out := make([]lspDiagnostic, 0, len(ds))
	for _, d := range ds {
		sev := 1
		if d.Sev == sevWarn {
			sev = 2
		}
		line := d.Line - 1
		if line < 0 {
			line = 0
		}
		col := d.Col - 1 // Col 0 (unattached) -> column 0
		if col < 0 {
			col = 0
		}
		out = append(out, lspDiagnostic{
			Range:    lspRange{Start: lspPosition{line, col}, End: lspPosition{line, col}},
			Severity: sev,
			Source:   "gopp",
			Message:  d.Msg,
		})
	}
	s.notify("textDocument/publishDiagnostics", map[string]any{
		"uri":         uri,
		"diagnostics": out,
	})
}

// ---------- position helpers ----------

// isIdentByte reports whether c continues a go++ identifier.
func isIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
		c >= '0' && c <= '9' || c == '_'
}

// identAt extracts the identifier surrounding pos in the document text.
func (d *lspDoc) identAt(pos lspPosition) (string, bool) {
	if pos.Line < 0 || pos.Line >= len(d.lines) {
		return "", false
	}
	line := d.lines[pos.Line]
	col := pos.Character
	if col > len(line) {
		col = len(line)
	}
	start := col
	for start > 0 && isIdentByte(line[start-1]) {
		start--
	}
	end := col
	for end < len(line) && isIdentByte(line[end]) {
		end++
	}
	if start == end {
		return "", false
	}
	return line[start:end], true
}

// colOf finds the first whole-word occurrence of name in the given
// 1-based source line, returning a 0-based column (0 when absent).
// Lets hover/definition point at the name where nodes carry no Col.
func (d *lspDoc) colOf(line1 int, name string) int {
	if line1 < 1 || line1 > len(d.lines) {
		return 0
	}
	line := d.lines[line1-1]
	for i := 0; i+len(name) <= len(line); i++ {
		j := strings.Index(line[i:], name)
		if j < 0 {
			break
		}
		j += i
		before := j == 0 || !isIdentByte(line[j-1])
		after := j+len(name) == len(line) || !isIdentByte(line[j+len(name)])
		if before && after {
			return j
		}
		i = j + 1
	}
	return 0
}

// findIdent locates an Ident expression with the given name on the given
// 1-based line, walking every declaration. When columns arrive in the
// AST this is where column disambiguation goes.
func (d *lspDoc) findIdent(name string, line1 int) *Ident {
	if d.f == nil {
		return nil
	}
	var found *Ident
	w := &astWalk{expr: func(e Expr) {
		if id, ok := e.(*Ident); ok && id.Name == name && id.Line == line1 && found == nil {
			found = id
		}
	}}
	for _, decl := range d.f.Decls {
		w.decl(decl)
	}
	return found
}

// ---------- requests ----------

func (s *lspServer) onHover(req *lspRequest) {
	var p positionParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.replyErr(req.ID, codeInvalidParams, err.Error())
		return
	}
	d := s.doc(p.TextDocument.URI)
	if d == nil {
		s.reply(req.ID, nil)
		return
	}
	name, ok := d.identAt(p.Position)
	if !ok {
		s.reply(req.ID, nil)
		return
	}
	line1 := p.Position.Line + 1
	text := ""
	if d.chk != nil {
		if id := d.findIdent(name, line1); id != nil {
			if ct, ok := d.chk.resolved[id]; ok {
				text = ct.enum.Name + " variant " + ct.variant.Name
			} else if t, ok := d.chk.types[id]; ok && !isErr(t) {
				text = name + ": " + d.typeString(t)
			}
		}
	}
	if text == "" {
		text = d.declHover(name, line1)
	}
	if text == "" {
		s.reply(req.ID, nil)
		return
	}
	s.reply(req.ID, &hoverResult{Contents: markupContent{Kind: "markdown", Value: "```gopp\n" + text + "\n```"}})
}

// typeString renders a hover type, qualifying foreign enum/struct decls
// with their package qualifier (geom.Point).
func (d *lspDoc) typeString(t Type) string {
	var decl Decl
	switch ty := t.(type) {
	case *tStruct:
		decl = ty.decl
	case *tEnum:
		decl = ty.decl
	}
	if decl != nil {
		if qual, ok := d.chk.declPkg[decl]; ok {
			return qual + "." + t.String()
		}
	}
	return t.String()
}

// declHover renders the declaration named name on line1 (func signature,
// enum/struct header), or "" if line1 holds no such declaration.
func (d *lspDoc) declHover(name string, line1 int) string {
	if d.f == nil {
		return ""
	}
	for _, decl := range d.f.Decls {
		switch dt := decl.(type) {
		case *FuncDecl:
			if dt.Name == name && dt.Line == line1 {
				return funcSignature(dt)
			}
		case *EnumDecl:
			if dt.Name == name && dt.Line == line1 {
				return "enum " + dt.Name
			}
		case *StructDecl:
			if dt.Name == name && dt.Line == line1 {
				return "type " + dt.Name + " struct"
			}
		}
	}
	return ""
}

// funcSignature renders `func name(p T, ...) R` from the declaration.
func funcSignature(fn *FuncDecl) string {
	fields := func(fs []Field) string {
		parts := make([]string, len(fs))
		for i, p := range fs {
			parts[i] = strings.TrimSpace(p.Name + " " + typeExprString(p.Type))
		}
		return strings.Join(parts, ", ")
	}
	sig := "func " + fn.Name + "(" + fields(fn.Params) + ")"
	if len(fn.Results) > 0 {
		sig += " " + fields(fn.Results)
	}
	return sig
}

// typeExprString from meta.go renders source-level type expressions.

func (s *lspServer) onDefinition(req *lspRequest) {
	var p positionParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.replyErr(req.ID, codeInvalidParams, err.Error())
		return
	}
	d := s.doc(p.TextDocument.URI)
	if d == nil || d.f == nil {
		s.reply(req.ID, nil)
		return
	}
	name, ok := d.identAt(p.Position)
	if !ok {
		s.reply(req.ID, nil)
		return
	}
	// Locals first: the nearest preceding declaration site with the same
	// name inside the enclosing function (sema.go declSites); fall back
	// to top-level decls and variant ctors.
	if d.chk != nil {
		if site, ok := d.localDef(name, p.Position); ok {
			col := site.col - 1
			if site.col <= 0 { // site has no column: scan the line text
				col = d.colOf(site.line, name)
			}
			rng := lspRange{
				Start: lspPosition{site.line - 1, col},
				End:   lspPosition{site.line - 1, col + len(name)},
			}
			s.reply(req.ID, []lspLocation{{URI: p.TextDocument.URI, Range: rng}})
			return
		}
	}
	line1 := 0
	for _, decl := range d.f.Decls {
		switch dt := decl.(type) {
		case *FuncDecl:
			if dt.Name == name {
				line1 = dt.Line
			}
		case *EnumDecl:
			if dt.Name == name {
				line1 = dt.Line
			}
		case *StructDecl:
			if dt.Name == name {
				line1 = dt.Line
			}
		}
	}
	if line1 == 0 && d.chk != nil {
		if ct, ok := d.chk.ctors[name]; ok {
			line1 = ct.variant.Line
		}
	}
	if line1 == 0 {
		s.reply(req.ID, nil)
		return
	}
	col := d.colOf(line1, name)
	rng := lspRange{
		Start: lspPosition{line1 - 1, col},
		End:   lspPosition{line1 - 1, col + len(name)},
	}
	s.reply(req.ID, []lspLocation{{URI: p.TextDocument.URI, Range: rng}})
}

// localDef resolves name at pos to its nearest preceding declaration
// site within the enclosing function. Approximation: the enclosing
// function is the nearest function-like decl at or above the cursor,
// and "nearest preceding wins" — block scopes are not modeled, so a
// name declared in a sibling branch could resolve to a site that isn't
// actually in scope at the cursor. In practice the common cases
// (params, :=, var, match bindings, receivers) resolve exactly, and
// shadowing picks the innermost declaration that precedes the use.
func (d *lspDoc) localDef(name string, pos lspPosition) (declSite, bool) {
	line1 := pos.Line + 1
	col1 := pos.Character + 1
	lower := d.enclosingDeclLine(line1)
	best := declSite{}
	found := false
	for _, ds := range d.chk.declSites {
		if ds.name != name || ds.line < lower || ds.line > line1 {
			continue
		}
		if ds.line == line1 && ds.col > col1 {
			continue // declared later on the same line (col 0 = unknown: allow)
		}
		if !found || ds.line > best.line || (ds.line == best.line && ds.col > best.col) {
			best, found = ds, true
		}
	}
	return best, found
}

// enclosingDeclLine returns the declaration line of the nearest
// function-like decl (func, impl method, behavior default body) at or
// above line1 — the lower bound for local definition lookup, so a local
// in one function never resolves into another.
func (d *lspDoc) enclosingDeclLine(line1 int) int {
	best := 0
	take := func(l int) {
		if l > 0 && l <= line1 && l > best {
			best = l
		}
	}
	for _, decl := range d.f.Decls {
		switch dt := decl.(type) {
		case *FuncDecl:
			take(dt.Line)
		case *ImplDecl:
			for _, m := range dt.Methods {
				take(m.Line)
			}
		case *BehaviorDecl:
			for _, m := range dt.Methods {
				if m.Body != nil {
					take(m.Line)
				}
			}
		}
	}
	return best
}

var lspKeywords = []string{
	"func", "enum", "type", "match", "loop", "for", "if", "else",
	"return", "break", "comptime", "import", "chan", "map",
}

var lspPrelude = []string{"Result", "Option", "Ok", "Err", "Some", "None", "ms", "second", "minute"}

func (s *lspServer) onCompletion(req *lspRequest) {
	var p positionParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.replyErr(req.ID, codeInvalidParams, err.Error())
		return
	}
	d := s.doc(p.TextDocument.URI)
	// After a typed qualifier (foo.), offer only that package's exported
	// symbols — the imported checker's funcs/types/ctors.
	if d != nil && d.chk != nil {
		if dep, ok := d.qualifierAt(p.Position); ok {
			s.reply(req.ID, qualifiedItems(dep))
			return
		}
	}
	items := make([]completionItem, 0, len(lspKeywords)+len(lspPrelude))
	for _, k := range lspKeywords {
		items = append(items, completionItem{Label: k, Kind: kindKeyword})
	}
	for _, n := range lspPrelude {
		items = append(items, completionItem{Label: n, Kind: kindVariable})
	}
	if d != nil && d.f != nil {
		for _, decl := range d.f.Decls {
			switch dt := decl.(type) {
			case *FuncDecl:
				items = append(items, completionItem{Label: dt.Name, Kind: kindFunction, Detail: funcSignature(dt)})
			case *EnumDecl:
				items = append(items, completionItem{Label: dt.Name, Kind: kindEnum, Detail: "enum"})
			case *StructDecl:
				items = append(items, completionItem{Label: dt.Name, Kind: kindClass, Detail: "struct"})
			}
		}
	}
	if d != nil && d.chk != nil {
		ctors := make([]string, 0, len(d.chk.ctors))
		for n := range d.chk.ctors {
			ctors = append(ctors, n)
		}
		sort.Strings(ctors) // deterministic output, not map order
		for _, n := range ctors {
			ct := d.chk.ctors[n]
			items = append(items, completionItem{Label: n, Kind: kindEnumMember, Detail: ct.enum.Name + " variant"})
		}
	}
	s.reply(req.ID, items)
}

// qualifierAt reports whether the cursor sits after `qual.` and returns
// the imported package's checker for that qualifier. Whole-line
// heuristic: scan back over the partial member name, a dot, then the
// qualifier identifier.
func (d *lspDoc) qualifierAt(pos lspPosition) (*checker, bool) {
	if pos.Line < 0 || pos.Line >= len(d.lines) {
		return nil, false
	}
	line := d.lines[pos.Line]
	i := pos.Character
	if i > len(line) {
		i = len(line)
	}
	isIdent := isIdentByte
	j := i
	for j > 0 && isIdent(line[j-1]) {
		j--
	}
	if j == 0 || line[j-1] != '.' {
		return nil, false
	}
	k := j - 1
	for k > 0 && isIdent(line[k-1]) {
		k--
	}
	if k == j-1 {
		return nil, false // dot with no qualifier before it
	}
	dep, ok := d.chk.imports[line[k:j-1]]
	return dep, ok
}

// qualifiedItems lists a dependency's exported symbols (§3: capitalized
// = exported): funcs, enums/structs, and variant ctors. Prelude types
// (Result/Option) are skipped — they're not members of the package.
func qualifiedItems(dep *checker) []completionItem {
	items := []completionItem{}
	funcs := make([]string, 0, len(dep.funcs))
	for n := range dep.funcs {
		if exported(n) {
			funcs = append(funcs, n)
		}
	}
	sort.Strings(funcs)
	for _, n := range funcs {
		items = append(items, completionItem{Label: n, Kind: kindFunction, Detail: dep.funcs[n].String()})
	}
	types := make([]string, 0, len(dep.enums)+len(dep.structs))
	for n, e := range dep.enums {
		if exported(n) && !dep.prelude[e] {
			types = append(types, n)
		}
	}
	for n := range dep.structs {
		if exported(n) {
			types = append(types, n)
		}
	}
	sort.Strings(types)
	for _, n := range types {
		kind := kindEnum
		detail := "enum"
		if _, isStruct := dep.structs[n]; isStruct {
			kind, detail = kindClass, "struct"
		}
		items = append(items, completionItem{Label: n, Kind: kind, Detail: detail})
	}
	ctors := make([]string, 0, len(dep.ctors))
	for n, ct := range dep.ctors {
		if exported(n) && !dep.prelude[ct.enum] {
			ctors = append(ctors, n)
		}
	}
	sort.Strings(ctors)
	for _, n := range ctors {
		items = append(items, completionItem{Label: n, Kind: kindEnumMember, Detail: dep.ctors[n].enum.Name + " variant"})
	}
	return items
}

func (s *lspServer) onDocumentSymbol(req *lspRequest) {
	var p docParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.replyErr(req.ID, codeInvalidParams, err.Error())
		return
	}
	d := s.doc(p.TextDocument.URI)
	if d == nil || d.f == nil {
		s.reply(req.ID, []documentSymbol{})
		return
	}
	syms := []documentSymbol{}
	for _, decl := range d.f.Decls {
		var name string
		kind := 0
		line1 := 0
		switch dt := decl.(type) {
		case *FuncDecl:
			name, kind, line1 = dt.Name, symFunction, dt.Line
		case *EnumDecl:
			name, kind, line1 = dt.Name, symEnum, dt.Line
		case *StructDecl:
			name, kind, line1 = dt.Name, symStruct, dt.Line
		}
		if kind == 0 {
			continue
		}
		col := d.colOf(line1, name)
		sel := lspRange{Start: lspPosition{line1 - 1, col}, End: lspPosition{line1 - 1, col + len(name)}}
		end := 0
		if line1 >= 1 && line1 <= len(d.lines) {
			end = len(d.lines[line1-1])
		}
		syms = append(syms, documentSymbol{
			Name:           name,
			Kind:           kind,
			Range:          lspRange{Start: lspPosition{line1 - 1, 0}, End: lspPosition{line1 - 1, end}},
			SelectionRange: sel,
		})
	}
	s.reply(req.ID, syms)
}

// ---------- AST walking ----------

// astWalk visits every expression in a declaration tree (type
// expressions excluded — no Ident nodes there). Used to find the Ident
// node under the cursor for hover.
type astWalk struct {
	expr func(Expr)
}

func (w *astWalk) decl(d Decl) {
	switch dt := d.(type) {
	case *FuncDecl:
		w.block(dt.Body)
	case *ImplDecl:
		for _, m := range dt.Methods {
			w.block(m.Body)
		}
	case *BehaviorDecl:
		for _, m := range dt.Methods {
			w.block(m.Body) // default method bodies (§23-lite)
		}
	case *ComptimeDecl:
		w.block(dt.Body)
	}
}

func (w *astWalk) block(b *Block) {
	if b != nil {
		w.stmts(b.List)
	}
}

func (w *astWalk) stmts(list []Stmt) {
	for _, s := range list {
		w.stmt(s)
	}
}

func (w *astWalk) stmt(s Stmt) {
	switch st := s.(type) {
	case *Block:
		w.stmts(st.List)
	case *VarStmt:
		w.e(st.Init)
	case *ExprStmt:
		w.e(st.X)
	case *AssignStmt:
		for _, e := range st.Lhs {
			w.e(e)
		}
		for _, e := range st.Rhs {
			w.e(e)
		}
	case *IfStmt:
		w.stmt(st.Init)
		w.e(st.Cond)
		w.block(st.Then)
		w.stmt(st.Else)
	case *ForStmt:
		w.stmt(st.Init)
		w.e(st.Cond)
		w.stmt(st.Post)
		w.block(st.Body)
	case *ForInStmt:
		w.e(st.X)
		w.block(st.Body)
	case *LoopStmt:
		w.block(st.Body)
	case *ReturnStmt:
		for _, e := range st.Results {
			w.e(e)
		}
	case *IncDecStmt:
		w.e(st.X)
	}
}

func (w *astWalk) pat(p Pattern) {
	switch pt := p.(type) {
	case *LiteralPat:
		w.e(pt.X)
	case *RecvPat:
		w.e(pt.Chan)
	case *SendPat:
		w.e(pt.Chan)
		w.e(pt.Value)
	case *AfterPat:
		w.e(pt.D)
	case *ClosedPat:
		w.e(pt.Chan)
	case *BoolPat:
		w.e(pt.X)
	}
}

func (w *astWalk) e(e Expr) {
	if e == nil {
		return
	}
	w.expr(e)
	switch ex := e.(type) {
	case *BinaryExpr:
		w.e(ex.X)
		w.e(ex.Y)
	case *UnaryExpr:
		w.e(ex.X)
	case *CallExpr:
		w.e(ex.Fun)
		for _, a := range ex.Args {
			w.e(a)
		}
	case *SelectorExpr:
		w.e(ex.X)
	case *IndexExpr:
		w.e(ex.X)
		for _, a := range ex.Index {
			w.e(a)
		}
	case *MakeChanExpr:
		w.e(ex.Cap)
	case *StructLitExpr:
		for _, fv := range ex.Fields {
			w.e(fv.Value)
		}
	case *SliceLitExpr:
		for _, v := range ex.Values {
			w.e(v)
		}
	case *TryExpr:
		w.e(ex.X)
	case *ComptimeExpr:
		w.e(ex.X)
	case *MatchExpr:
		w.e(ex.Subject)
		for _, arm := range ex.Arms {
			w.pat(arm.Pat)
			w.e(arm.Guard)
			w.stmts(arm.Body)
			w.e(arm.BodyExpr)
		}
	}
}
