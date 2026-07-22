package main

import (
	"embed"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// playground holds the browser playground assets (editor + wasm loader).
//
//go:embed playground/*
var playgroundFS embed.FS

// runPlay implements `gopp play`: build the compiler to js/wasm, assemble
// a docroot (playground assets + gopp.wasm + GOROOT's wasm_exec.js) in a
// temp dir, and serve it on localhost. There is no Go toolchain in the
// browser, so the page compiles go++ to Go source and shows diagnostics —
// it does not run the program.
func runPlay(args []string) int {
	if len(args) > 1 {
		fmt.Fprintln(os.Stderr, "usage: gopp play [-addr host:port]")
		return 2
	}
	addr := "localhost:8585"
	if len(args) == 1 {
		if !strings.HasPrefix(args[0], "-addr=") {
			fmt.Fprintln(os.Stderr, "usage: gopp play [-addr host:port]")
			return 2
		}
		addr = strings.TrimPrefix(args[0], "-addr=")
	}
	if _, err := exec.LookPath("go"); err != nil {
		fmt.Fprintln(os.Stderr, "gopp play: go toolchain not found on PATH")
		return 1
	}

	dir, err := os.MkdirTemp("", "gopp-play-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "gopp:", err)
		return 1
	}
	defer os.RemoveAll(dir)

	// build the compiler itself to wasm; the package must be the current
	// directory (i.e. run `gopp play` from the repo checkout)
	fmt.Println("building gopp.wasm ...")
	wasm := filepath.Join(dir, "gopp.wasm")
	cmd := exec.Command("go", "build", "-o", wasm, ".")
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "gopp play: wasm build failed: %v\n%s", err, out)
		return 1
	}

	// wasm_exec.js is not vendored: copy it from GOROOT at serve time
	goroot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gopp play: go env GOROOT:", err)
		return 1
	}
	wexec, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(goroot)), "misc", "wasm", "wasm_exec.js"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "gopp play:", err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(dir, "wasm_exec.js"), wexec, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "gopp:", err)
		return 1
	}

	// unpack the embedded playground/ assets next to the wasm files
	sub, err := fs.Sub(playgroundFS, "playground")
	if err != nil {
		fmt.Fprintln(os.Stderr, "gopp:", err)
		return 1
	}
	if err := copyFS(dir, sub); err != nil {
		fmt.Fprintln(os.Stderr, "gopp:", err)
		return 1
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gopp play:", err)
		return 1
	}
	url := "http://" + ln.Addr().String() + "/"
	fmt.Printf("serving the go++ playground at %s (Ctrl+C to stop)\n", url)
	if err := http.Serve(ln, http.FileServer(http.Dir(dir))); err != nil {
		fmt.Fprintln(os.Stderr, "gopp play:", err)
		return 1
	}
	return 0
}

// copyFS writes every file in fsys (walked from ".") into dir.
func copyFS(dir string, fsys fs.FS) error {
	return fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
}
