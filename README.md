# go++

go++ is a strict-typed language that transpiles to Go. It keeps Go's
runtime and toolchain, deletes Go's footguns, and adds the things Go
won't: sum types, generics done sanely, traits, comptime
metaprogramming, and Pony-style actors. The output is a plain Go
module you can read, vet, race-check, and debug with delve.

```
package main

enum Status {
    Active
    Banned(reason string)
}

actor Counter {
    count int                       // private state

    be Add(n int) {                 // async behavior — a message, not a call
        count = count + n
    }
}

func describe(s Status) string {
    return match s {                // exhaustiveness-checked, or it doesn't compile
    Active -> "active"
    Banned(r) -> "banned: {r}"      // string interpolation
    }
}

func main() {
    c := Counter{}                  // spawns the actor (compiler's `go`, never yours)
    c.Add(42)
    println(describe(Banned("spam")))
}
```

## Why

Go's ergonomics, minus its worst ideas:

- **Sum types with exhaustiveness.** `enum` with payloads and generics;
  `match` must cover every variant. `Result[T, E]` and `Option[T]` are
  in the prelude — `error` and `nil` maps are not in the language.
- **`?` instead of `if err != nil`.** Failures are values, propagated
  with `?`, handled with `match`.
- **Strict numerics.** `int8 + int64` is a compile error; conversions
  are explicit. Untyped literals with compile-time overflow checks.
- **Real generics.** `func Identity[T](x T) T`, `enum Box[T]`,
  `type Pair[T] struct` — checked once, rigidly, inferred at call
  sites. Emitted as Go generics.
- **Behaviors (traits).** `behavior`/`impl`/bounds, default methods,
  operator overloading (`impl Add for Vec2` makes `a + b` work),
  `index`/`set` for custom containers.
- **Comptime metaprogramming.** Top-level `comptime { }` blocks walk,
  use, and rewrite the package's own declarations before checking —
  generate functions, mutate signatures, call declared functions at
  compile time. `comptime embed("file")` bakes files into the binary.
- **Actors.** Pony-style concurrency: private state, async behaviors,
  sequential execution per actor, sendability checked at the boundary
  (no pointers, maps, or slices cross actors). No goroutine keyword.
- **Type syntax that reads well.** `map<K, V>`, `chan<T>`, `[T]`,
  `Pair[int]` — not Go's bracket soup.

## Quick start

Requires Go 1.23+.

```
git clone https://github.com/lantos1618/gopp
cd gopp
go build -o gopp .

./gopp run examples/hello.gopp          # compile + run in one step
./gopp build examples/hello.gopp -o hello && ./hello
./gopp test examples/testdemo           # run *_test.gopp files
./gopp fmt -w examples/                 # canonical formatting
./gopp lsp                              # language server (VS Code below)
./gopp play                             # browser playground at :8585
```

## A tour

**Errors are values.** `func fetch(id int) Result[User, string]` with
`?` propagation and `r.IsOk()` / `match` handling. The `error` type,
`any`, and bare `<-` don't exist.

**Strict by default.** No implicit conversions, no nil maps (declared
maps are made, always), no uninitialized anything that matters —
except deliberate zero values, which are honest.

**Channels without `<-`.** `ch.send(v)`, `ch.recv()`, `ch.close()`;
`match { v := ch.recv() -> ..., after(1 * second) -> ... }` is select.
Range loops over slices, maps, and channels: `for x in xs { }`.

**Comptime.** `comptime 1 << 20` folds at compile time. Top-level
`comptime { }` blocks metaprogram: `for d in decls() { ... }` with live
handles — rename functions, add parameters, generate whole types.
`examples/jsondemo` generates per-struct JSON marshal/unmarshal this
way — Go's `encoding/json` without `any` or reflection.

**Actors.** `actor` + `be` — private state, async behaviors, messages
checked sendable. Pony's rule simplified: nothing deep-mutable crosses.

**Self-hosted programs.** The formatter (`programs/gofmt`) and the
lexer (`programs/goplex`) are written in go++ itself.

## Standard library

`str` `conv` `math` `os` `time` `sort` `rand` `filepath` `json` —
declared in go++, implemented natively via `= native` FFI where needed.
`json` includes a pure-go++ parser; struct codecs are comptime-generated.

Language string builtins work at runtime and comptime with the same
names: `contains`, `has_prefix`, `has_suffix`, `replace`, `split`,
`join`, `upper`, `lower`, `trim`, `repeat`.

## Tooling

| command | what it does |
|---|---|
| `gopp <file.gopp> [-o dir]` | compile to a Go module |
| `gopp run <file.gopp>` | compile + run |
| `gopp build <file.gopp> [-o bin]` | compile to a binary |
| `gopp test [dir]` | run `*_test.gopp` (`assert`, `assertEq`) |
| `gopp fmt [-w] <files>` | canonical formatter (idempotent) |
| `gopp lsp` | language server: diagnostics, hover, defs, completion |
| `gopp play` | WASM playground in the browser |

Editor support: `editors/vscode` (highlighting + LSP client).
Generated code is plain Go — `go run -race .`, vet, delve, and pprof
all work on it.

## Docs

- `SPEC.md` — the language, written down (semantics, rules, limits)
- `ROADMAP.md` — skeleton coverage and what's deliberately declined
- `examples/` — every feature, runnable
- `tests/ui/` — the diagnostics, as a test suite

## Status

Actively built. Core type system complete (generics, behaviors,
operators, comptime); concurrency via actors landed; stdlib and
self-hosting growing. Main is protected: CI (gofmt, vet, tests, build)
gates every push.
