//go:build !js || !wasm

package main

// wasmMode is false in native builds; wasm_js.go sets it true under
// js/wasm, where main() blocks after registering the goppCompile global.
var wasmMode = false
