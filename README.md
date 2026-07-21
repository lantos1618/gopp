# go++

go++ is a strict-typed Go derivative that transpiles to Go. It keeps Go's
runtime, toolchain, and concurrency model, and deletes the footguns at the
language level:

- **Sum types with exhaustiveness checking.** `enum` with payloads and
  generics; `match` must cover every variant or it doesn't compile.
- **`Result[T, E]` instead of `error` returns.** `error` is not a type in
  go++. Failures are values, propagated with `?`, handled with `match`.
- **No nil maps.** `var m map[K]V` emits `make(...)` — a declared map is
  always ready to write.
- **No implicit conversions.** `int8 + int64` is a compile error;
  conversions are explicit (`int64(x)`). Numeric literals are untyped
  constants with compile-time overflow checks.
- **No `<-` operator.** Channels are used through methods:
  `ch.send(v)`, `ch.recv()`, `ch.close()`; `match` without a subject is
  the select.
- **Comptime metaprogramming.** `comptime expr` folds constants; top-level
  `comptime { }` blocks walk and rewrite the package's declarations before
  type checking runs.

The output is a plain Go module — no runtime, no codegen magic you can't
read.

## Quick start

Requires Go 1.23+.

```
go build -o gopp .
./gopp examples/hello.gopp && cd gopp-out && go run .
```

or in one step:

```
./gopp run examples/hello.gopp
```

`gopp run` compiles to a temp dir and runs it via `go run .`, streaming
stdout/stderr and propagating the exit code. It needs `go` on PATH.

## Feature tour

Sum types + exhaustive match (from `examples/hello.gopp`):

```go
enum Status {
    Pending
    Active
    Failed(reason string)
}

match s {
    Pending -> println("waiting...")
    Active -> println("live")
    Failed(reason) -> println("dead: " + reason)
}
```

Leave out an arm and the compiler tells you. Guards never count toward
coverage, and shadowed arms are `unreachable pattern` warnings.

`Result` and `?` (from `examples/try.gopp`):

```go
func build(id int) Result[int, string] {
    u := fetchUser(id)?
    p := fetchPerms(u)?
    return Ok[int, string](u + p)
}
```

`?` desugars to the nested match propagation; a function that can fail
says so in its return type. Type arguments infer from context:
`var r Result[int, string] = Ok(1)`.

Channels + match-as-select, and maps that can't be nil (from
`examples/features.gopp`):

```go
ch := chan[int](1)
ch.send(42)
got := match {
    v := ch.recv() -> v
    after(1 * second) -> -1
}

var scores map[string]int
scores["alice"] = 90 // no nil-map panic
```

Comptime metaprogramming (from `examples/meta.gopp`) — blocks run during
sema, before any name resolution, so what they mutate is what checking and
codegen see:

```go
comptime {
    for d in decls() {
        if d.kind == "enum" && d.name == "Color" {
            d.variants.add(Variant("Neon")) // matches must now cover Neon
        }
    }
    n := fib(10) // declared functions are callable at comptime
    f := Func("fib10")
    f.results.add(Param("", "int"))
    f.body = "return " + str(n)
    gen(f) // inject a new function into the package
}
```

Packages are directories (see `examples/imports/`): `import "geom"` loads
`./geom`; the qualifier is the dependency's package name; capitalized =
exported; cycles are errors.

## Tooling

```
gopp <input.gopp> [-o outdir]   compile to a Go module (default ./gopp-out)
gopp run <input.gopp>           compile to a temp dir and run it
gopp fmt [-w] <files...>        reformat go++ source (-w writes in place)
```

The emitted code is ordinary Go in an ordinary module, so the whole Go
toolchain works on it. To check a program under the race detector:

```
./gopp examples/hello.gopp && cd gopp-out && go run -race .
```

Same goes for `go vet`, delve, pprof — point them at `gopp-out`.

## Docs

- `SPEC.md` — the language contract: every rule the checker enforces,
  written down.
- `ROADMAP.md` — skeleton coverage and what's deliberately deferred.
- `examples/` — runnable programs for each feature, with expected output
  in the header comments.

## Tests

```
go test ./...
```

`tests/ui/*.gopp` holds diagnostics tests (`//~ ERROR msg` annotations,
checked both directions), plus a fuzz test (the compiler must diagnose,
never panic) and an end-to-end test that compiles and runs the examples.
