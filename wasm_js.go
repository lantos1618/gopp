//go:build js && wasm

package main

import "syscall/js"

// wasmMode is true in the js/wasm build: main() blocks forever after the
// goppCompile global is registered instead of parsing CLI args (there is
// no argv in the browser).
var wasmMode = true

func init() {
	js.Global().Set("goppCompile", js.FuncOf(wasmCompile))
}

// wasmCompile runs the frontend pipeline (lex -> parse -> checkImports ->
// emit) on src and returns {go: string, diags: string} to the browser. On
// any error the "go" field is empty and diags carries the rendered
// diagnostics; warnings alone still produce output.
func wasmCompile(this js.Value, args []js.Value) any {
	src := ""
	if len(args) > 0 {
		src = args[0].String()
	}
	diags := &Diagnostics{}
	toks, err := lex(src)
	if err != nil {
		diagFromError(diags, err)
		return wasmResult("", diags.Render(src))
	}
	file, parseDiags := parse(toks)
	diags.items = append(diags.items, parseDiags.items...)
	if diags.HasErrors() {
		// syntax errors: don't run sema on a partial AST (same policy
		// as the CLI in main.go)
		return wasmResult("", diags.Render(src))
	}
	chk, semDiags := checkImports(file, nil, nil, checkOpts{src: src})
	diags.items = append(diags.items, semDiags.items...)
	if diags.HasErrors() {
		return wasmResult("", diags.Render(src))
	}
	return wasmResult(emit(file, chk), diags.Render(src))
}

func wasmResult(goSrc, diags string) any {
	return map[string]any{"go": goSrc, "diags": diags}
}
