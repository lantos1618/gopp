package main

// lsp_test.go — drives serveLSP over in-memory buffers: full requests in,
// framed messages out, matched by id/method in order (the server is
// single-threaded, so output order is deterministic).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// lspMsg is a decoded output frame (response or notification).
type lspMsg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *lspError       `json:"error"`
	Params json.RawMessage `json:"params"`
}

// runLSP feeds requests (already JSON bodies) to serveLSP and returns
// the decoded output frames in order.
func runLSP(t *testing.T, requests ...string) []lspMsg {
	t.Helper()
	var in, out bytes.Buffer
	for _, r := range requests {
		fmt.Fprintf(&in, "Content-Length: %d\r\n\r\n%s", len(r), r)
	}
	if err := serveLSP(&in, &out); err != nil {
		t.Fatalf("serveLSP: %v", err)
	}
	var msgs []lspMsg
	br := bufio.NewReader(bytes.NewReader(out.Bytes()))
	for {
		body, err := readFrame(br)
		if err != nil {
			break // io.EOF: everything parsed
		}
		var m lspMsg
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("bad output frame: %v", err)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func req(id int, method string, params any) string {
	p, _ := json.Marshal(params)
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, p)
}

func note(method string, params any) string {
	p, _ := json.Marshal(params)
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":%s}`, method, p)
}

func initReq(id int) string {
	return req(id, "initialize", map[string]any{
		"processId": nil,
		"rootUri":   nil,
	})
}

func docURI() string { return "file:///test.gopp" }

func didOpen(src string) string {
	return note("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": docURI(), "languageId": "gopp", "version": 1, "text": src,
		},
	})
}

func posReq(id int, method string, line, char int) string {
	return req(id, method, map[string]any{
		"textDocument": map[string]any{"uri": docURI()},
		"position":     map[string]any{"line": line, "character": char},
	})
}

// byID finds the response frame for a request id.
func byID(t *testing.T, msgs []lspMsg, id int) lspMsg {
	t.Helper()
	want := fmt.Sprintf("%d", id)
	for _, m := range msgs {
		if string(m.ID) == want {
			return m
		}
	}
	t.Fatalf("no response for id %d in %+v", id, msgs)
	return lspMsg{}
}

// byMethod finds the first notification frame with the given method.
func byMethod(t *testing.T, msgs []lspMsg, method string) lspMsg {
	t.Helper()
	for _, m := range msgs {
		if m.Method == method {
			return m
		}
	}
	t.Fatalf("no %s notification in %+v", method, msgs)
	return lspMsg{}
}

func TestLSPInitialize(t *testing.T) {
	msgs := runLSP(t, initReq(1), req(2, "shutdown", nil), note("exit", nil))
	m := byID(t, msgs, 1)
	if m.Error != nil {
		t.Fatalf("initialize error: %+v", m.Error)
	}
	var res struct {
		Capabilities struct {
			TextDocumentSync       int  `json:"textDocumentSync"`
			HoverProvider          bool `json:"hoverProvider"`
			DefinitionProvider     bool `json:"definitionProvider"`
			DocumentSymbolProvider bool `json:"documentSymbolProvider"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(m.Result, &res); err != nil {
		t.Fatalf("initialize result: %v", err)
	}
	c := res.Capabilities
	if c.TextDocumentSync != 1 || !c.HoverProvider || !c.DefinitionProvider || !c.DocumentSymbolProvider {
		t.Fatalf("bad capabilities: %+v", c)
	}
	if sh := byID(t, msgs, 2); sh.Error != nil {
		t.Fatalf("shutdown error: %+v", sh.Error)
	}
}

func TestLSPMethodNotFound(t *testing.T) {
	msgs := runLSP(t, initReq(1), req(2, "textDocument/rename", nil), note("exit", nil))
	m := byID(t, msgs, 2)
	if m.Error == nil || m.Error.Code != codeMethodNotFound {
		t.Fatalf("want MethodNotFound, got %+v", m)
	}
}

// Requests on unopened documents must not crash and answer null.
func TestLSPUnknownDoc(t *testing.T) {
	msgs := runLSP(t, initReq(1),
		posReq(2, "textDocument/hover", 0, 0),
		posReq(3, "textDocument/definition", 0, 0),
		note("exit", nil))
	if m := byID(t, msgs, 2); m.Error != nil || string(m.Result) != "null" {
		t.Fatalf("hover on unopened doc: %+v", m)
	}
	if m := byID(t, msgs, 3); m.Error != nil || string(m.Result) != "null" {
		t.Fatalf("definition on unopened doc: %+v", m)
	}
}

const lspBadSrc = `package main

enum Status {
    Ready
    Failed
}

func main() {
    var s Status = Ready
    var n int = "nope"
    println(s)
}
`

func TestLSPDiagnostics(t *testing.T) {
	msgs := runLSP(t, initReq(1), note("initialized", nil), didOpen(lspBadSrc), note("exit", nil))
	m := byMethod(t, msgs, "textDocument/publishDiagnostics")
	var p struct {
		URI         string          `json:"uri"`
		Diagnostics []lspDiagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		t.Fatalf("diagnostics params: %v", err)
	}
	if p.URI != docURI() {
		t.Fatalf("wrong uri %q", p.URI)
	}
	if len(p.Diagnostics) != 1 {
		t.Fatalf("want 1 diagnostic, got %d: %+v", len(p.Diagnostics), p.Diagnostics)
	}
	d := p.Diagnostics[0]
	// `var n int = "nope"` is line 10 in the source (0-based 9).
	if d.Range.Start.Line != 9 || d.Severity != 1 {
		t.Fatalf("wrong diagnostic position/severity: %+v", d)
	}
	if !strings.Contains(d.Message, "string") {
		t.Fatalf("diagnostic should mention string: %q", d.Message)
	}
}

func TestLSPDidChangeClearsDiagnostics(t *testing.T) {
	fixed := strings.Replace(lspBadSrc, `"nope"`, "42", 1)
	change := note("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": docURI(), "version": 2},
		"contentChanges": []map[string]any{{"text": fixed}},
	})
	msgs := runLSP(t, initReq(1), didOpen(lspBadSrc), change, note("exit", nil))
	count := 0
	for _, m := range msgs {
		if m.Method != "textDocument/publishDiagnostics" {
			continue
		}
		var p struct {
			Diagnostics []lspDiagnostic `json:"diagnostics"`
		}
		json.Unmarshal(m.Params, &p)
		count++
		if count == 2 && len(p.Diagnostics) != 0 {
			t.Fatalf("didChange should clear diagnostics, got %+v", p.Diagnostics)
		}
	}
	if count != 2 {
		t.Fatalf("want 2 publishDiagnostics, got %d", count)
	}
}

func TestLSPHover(t *testing.T) {
	msgs := runLSP(t, initReq(1), didOpen(lspBadSrc),
		posReq(2, "textDocument/hover", 10, 12), // `s` in println(s)
		note("exit", nil))
	m := byID(t, msgs, 2)
	if m.Error != nil {
		t.Fatalf("hover error: %+v", m.Error)
	}
	var h hoverResult
	if err := json.Unmarshal(m.Result, &h); err != nil {
		t.Fatalf("hover result: %v", err)
	}
	if !strings.Contains(h.Contents.Value, "s: Status") {
		t.Fatalf("hover should show s: Status, got %q", h.Contents.Value)
	}
}

func TestLSPHoverFuncSignature(t *testing.T) {
	src := `package main

func classify(n int) string {
    return "x"
}

func main() {
    println(classify(1))
}
`
	msgs := runLSP(t, initReq(1), didOpen(src),
		posReq(2, "textDocument/hover", 2, 6), // `classify` decl name
		note("exit", nil))
	m := byID(t, msgs, 2)
	var h hoverResult
	if err := json.Unmarshal(m.Result, &h); err != nil {
		t.Fatalf("hover result: %v", err)
	}
	if !strings.Contains(h.Contents.Value, "func classify(n int) string") {
		t.Fatalf("hover should show the signature, got %q", h.Contents.Value)
	}
}

func TestLSPDefinition(t *testing.T) {
	src := `package main

enum Status {
    Ready
    Failed
}

func main() {
    var s Status = Ready
    println(s)
}
`
	// `Ready` usage at 0-based line 8; variant decl at line 3 (0-based).
	msgs := runLSP(t, initReq(1), didOpen(src),
		posReq(2, "textDocument/definition", 8, strings.Index("    var s Status = Ready", "Ready")),
		posReq(3, "textDocument/definition", 8, strings.Index("    var s Status = Ready", "Status")), // `Status` usage
		note("exit", nil))
	m := byID(t, msgs, 2)
	var locs []lspLocation
	if err := json.Unmarshal(m.Result, &locs); err != nil {
		t.Fatalf("definition result: %v", err)
	}
	if len(locs) != 1 || locs[0].Range.Start.Line != 3 {
		t.Fatalf("Ready should define at line 3, got %+v", locs)
	}
	m3 := byID(t, msgs, 3)
	locs = nil
	if err := json.Unmarshal(m3.Result, &locs); err != nil {
		t.Fatalf("definition result: %v", err)
	}
	if len(locs) != 1 || locs[0].Range.Start.Line != 2 {
		t.Fatalf("Status should define at line 2, got %+v", locs)
	}
}

func TestLSPCompletion(t *testing.T) {
	src := `package main

enum Status {
    Ready
    Failed
}

func helper() {
}

func main() {
}
`
	msgs := runLSP(t, initReq(1), didOpen(src), posReq(2, "textDocument/completion", 0, 0), note("exit", nil))
	m := byID(t, msgs, 2)
	var items []completionItem
	if err := json.Unmarshal(m.Result, &items); err != nil {
		t.Fatalf("completion result: %v", err)
	}
	have := map[string]bool{}
	for _, it := range items {
		have[it.Label] = true
	}
	for _, want := range []string{"func", "match", "Result", "None", "ms", "helper", "Status", "Ready", "Failed", "main"} {
		if !have[want] {
			t.Fatalf("completion missing %q (have %d items)", want, len(items))
		}
	}
}

func TestLSPDocumentSymbol(t *testing.T) {
	src := `package main

enum Status {
    Ready
    Failed
}

type User struct {
    ID int
}

func main() {
}
`
	msgs := runLSP(t, initReq(1), didOpen(src),
		req(2, "textDocument/documentSymbol", map[string]any{
			"textDocument": map[string]any{"uri": docURI()},
		}),
		note("exit", nil))
	m := byID(t, msgs, 2)
	var syms []documentSymbol
	if err := json.Unmarshal(m.Result, &syms); err != nil {
		t.Fatalf("documentSymbol result: %v", err)
	}
	if len(syms) != 3 {
		t.Fatalf("want 3 symbols, got %+v", syms)
	}
	got := map[string]int{}
	for _, sy := range syms {
		got[sy.Name] = sy.Kind
	}
	if got["Status"] != symEnum || got["User"] != symStruct || got["main"] != symFunction {
		t.Fatalf("wrong symbol kinds: %+v", syms)
	}
	if syms[0].Range.Start.Line != 2 { // Status decl line
		t.Fatalf("wrong symbol range: %+v", syms[0])
	}
}

func TestLSPMalformedFrame(t *testing.T) {
	in := strings.NewReader("Content-Length: nope\r\n\r\n{}")
	var out bytes.Buffer
	if err := serveLSP(in, &out); err == nil {
		t.Fatal("malformed Content-Length should error")
	}
	// truncated body
	in2 := strings.NewReader("Content-Length: 100\r\n\r\n{}")
	if err := serveLSP(in2, &out); err == nil {
		t.Fatal("truncated body should error")
	}
	// clean EOF is not an error
	if err := serveLSP(strings.NewReader(""), &out); err != nil {
		t.Fatalf("empty stream: %v", err)
	}
}

func TestLSPNoCrashBeforeInit(t *testing.T) {
	// hover before initialize: answered, not a crash
	msgs := runLSP(t, posReq(1, "textDocument/hover", 0, 0), note("exit", nil))
	m := byID(t, msgs, 1)
	if m.Error != nil {
		t.Fatalf("pre-init hover: %+v", m.Error)
	}
}
