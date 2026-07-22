# go++ language & compiler spec (§29 — decisions, written down)

This is the contract the compiler enforces. If the checker can't write a
rule down as "if X and Y, then Z", it's a bug farm — so here they all are.
Section numbers refer to the ZEN SEMA SKELETON this compiler follows.

## Pipeline

`lex -> parse -> check -> emit Go -> go toolchain`

- **§0 pass architecture.** Three sema passes: collect declarations (enums,
  function signatures), check bodies, flow checks. Diagnostics are collected
  from every pass and printed together; codegen runs only with zero errors.
- **§11 never stop at the first error.** A failed check records one
  diagnostic and yields the poison type `tError`, which unifies silently
  with everything. One bug = one diagnostic, no cascades.
- **§11 diagnostics are for humans.** The default rendering shows the
  source line with a caret (`line 3:10: error: ...` + snippet) — columns
  are exact for syntax errors; sema errors are line-level (documented,
  since expressions carry lines only). Secondary labels explain the
  primary error: return mismatches point at the declared return type,
  redeclarations at the previous declaration. Undefined names get a
  deterministic edit-distance "did you mean X?" suggestion.
- **§1 side tables.** Expression types, resolved variant constructors, and
  pattern roles live in maps on the checker, not in the AST.

## Types (§4)

- Nominal for enums: identity is the declaration, not the shape.
- `tNever` (bottom): the type of `panic(...)`. Unifies with any type, so
  `Err(e) -> panic(e)` is a valid value-producing match arm and
  `loop {}` without a break satisfies all-paths-return.
- No implicit conversions (§7): `int + string`, `1 < "x"`, `true && 1` are
  errors, and so is mixing typed numerics (`int8 + int64`). There is no
  truthiness; conditions must be `bool`. Explicit conversions exist —
  see §7 below.
- Aliases: none in the language. Generics: enum declarations only
  (`Result[T, E]`). Use sites take explicit type arguments
  (`Ok[int, string](1)`) or infer them (§8-lite below). Arity is
  checked (§5).

## Inference & literals (§6, §7)

- Bidirectional checking: expressions infer bottom-up; declarations,
  return statements, call arguments, and match-in-value-context are
  CHECK mode — the expected type propagates downward. Signatures and
  declarations are the blame boundaries.
- Numeric literals are **untyped constants**: inferring `5` yields
  `untyped int`, `1.5` yields `untyped float`. CHECK mode adopts the
  expected numeric type with a compile-time overflow check
  (`var x int8 = 300` is an error, including signed literals like
  `var b uint8 = -1`). Unconstrained use defaults: `int` / `float64`.
- In arithmetic and comparison an untyped constant yields to the typed
  operand (`a + 1` has `a`'s type); two untyped constants stay untyped
  until defaulted. `duration` absorbs any numeric operand (it is an
  int64 count; `d * 3` must stay convenient). `%`, bit ops, and shifts
  require integer operands — no float `%`, no float shift counts.
- **No implicit conversions between typed values.** `int8 + int64` and
  `int32 < int64` are "mismatched types" errors. The escape hatch is an
  explicit conversion, `int64(x)` — a basic type name in call position.
  Numeric↔numeric converts freely; `rune`↔`string` converts;
  `string(int)` is rejected (did you mean `string(rune(...))`?);
  everything else is a "cannot convert" error. Identity conversions are
  allowed.
- Inference is function-local and annotation-free: `:=` takes the RHS
  type directly (defaulting untyped literals). There are no inference
  variables, hence no unification engine and no occurs check — generic
  instantiation (below) is pattern matching, not unification.

## Generic functions (§8)

- `func Identity[T](x T) T` — type parameters in brackets after the
  name. The body is checked ONCE against the rigid parameters: a `T`
  value may be passed, returned, stored in containers, and put into
  generic enum instantiations — nothing else (no arithmetic, no
  comparison, no field access; that needs behaviors, still deferred).
- Call sites instantiate by inference or explicitly: `Identity(42)`,
  `Identity[bool](true)`. Inference pattern-matches argument types
  against parameter patterns (parameters may nest inside
  enums/maps/slices/chans/pointers), seeded by the expected type, with
  the same rules as constructor inference: unsolved parameters are an
  error ("cannot infer type argument T for f"), conflicts diagnose once
  ("type argument T inferred as both string and untyped int"), untyped
  literal constraints yield to typed ones.
- A generic function named without a call is an error ("generic
  function f needs type arguments") — no uninstantiated function values.
- Emission maps to Go generics directly (`func Identity[T any](x T) T`),
  so instantiation and monomorphization are the Go toolchain's job.

## Operator overloading (§14)

- The operator behaviors live in the prelude: `Add Sub Mul Div Mod`
  (`+ - * / %`), `Eq` (`==`, `!=` — `!=` negates `eq`), `Ord`
  (`< <= > >=` — desugared to `cmp(x, y) <op> 0`), `Neg` (unary `-`),
  `Not` (unary `!`). They cannot be redeclared.
- Implementing one enables the operator on the type:
  `impl Add for Vec2 { add(self, rhs Vec2) Vec2 { ... } }`. An impl
  WINS over the built-in rules; without one, user types fall through to
  the ordinary errors ("invalid operation: Vec2 + Vec2").
- Bounds make operators work on rigid type parameters:
  `func Sum[T: Add](a T, b T) T { return a + b }`.
- Emission desugars to method calls and writes the prelude interfaces
  only when used: `type Add[T any] interface { add(T) T }`; a bound
  `T: Add` becomes the Go constraint `T Add[T]`.
- Compound assignment desugars too: `v += u` is `v = v.add(u)`. A
  compound-assignable type without the impl is now a sema error (it used
  to slip through to a Go compile error — the leak is closed).
- Indexing overloads: an impl defining `index` enables `g[i, j]` reads
  (any arity, any element type — the behavior's signatures are
  user-defined); `set` enables `g[i] = v` writes. Write without `set` is
  "cannot assign to index (no set method)"; compound assignment to an
  overloaded index is rejected (no read-modify-write desugar).
- Deferred: shifts on user types, cross-package impls (Go methods must
  live in the type's package — deliberately NOT relaxed, like Rust's
  orphan rule but stricter).

## Behaviors (§8)

- `behavior Stringer { String(self) string }` — a trait. The first
  parameter of every method is the receiver. A behavior lowers to a Go
  interface.
- `impl Stringer for Status { ... }` — receiver methods on the Go type.
  Validation: the behavior must exist, the target must be a LOCAL
  non-generic enum or struct (the orphan rule), one impl per
  (behavior, type), one method name per type (Go emission makes this a
  hard rule, not a preference), every behavior method implemented with
  the behavior's exact signature (Self = the concrete type), no extras.
- Method calls work on concrete types (`s.String()`) and on rigid type
  parameters under a bound: `func Shout[T: Stringer](x T) string {
  return x.String() + "!" }`. A bound lowers to a Go constraint.
- Instantiation checks the bound: `Shout(1)` → "int does not implement
  Stringer (bound of Shout's T)". Basic types never implement behaviors
  (orphan rule again).
- Generic impls: `impl Shower for Box[T]` — the enum's own parameters,
  in order; methods use `T` rigidly, signatures instantiate per call
  site. Emission is a Go receiver method on `Box[T]`.
- Default method bodies (§23-lite): a behavior method with a body is a
  fallback — impls that omit it get it, impls that provide it override
  it. Defaults are checked once, rigidly: the receiver is Self with the
  behavior as its bound, so a default may call sibling methods but has
  no fields. Emission writes the default as the receiver method.
- Deferred: imported behaviors/types in impls, multiple bounds per
  parameter.

## Generic constructor inference (§8-lite)

- `var r Result[int, string] = Ok(1)`, `return Ok(1)` from a
  `Result[int, string]` function, and `var o Option[int] = None` all
  work without explicit type arguments. The expected type seeds the
  solution; value-argument types are then pattern-matched against the
  variant's field types (parameters may nest inside
  enums/maps/slices/chans/pointers).
- What cannot be solved is an error, not a guess: `println(Ok(1))` →
  "cannot infer type argument E for Ok; use explicit Ok[T, E](...)".
  A bare generic unit variant in infer mode (`n := None`) stays an
  error.
- Conflicts diagnose once and poison the parameter: `Ok("x")` against
  `Result[int, string]` → "type argument T inferred as both int and
  string". One mistake, one diagnostic (§11).
- Untyped literal constraints yield to typed ones and default at the
  end (§7), so `Ok(1)` against `Result[int64, string]` solves T=int64
  and the literal still gets its overflow check.

## Standard library & native FFI

- `import "str"` / `import "conv"` name no directory on disk, so the
  compiler's embedded registry (stdlib.go) provides the package: a .gopp
  API declaration plus a native Go implementation. A real directory with
  the same name always wins.
- `func ToUpper(s string) string = native` — the declaration is checked
  normally; the body comes from the package's native.go. `= native` is
  stdlib-only; user code gets "= native is only allowed in the standard
  library".
- `str`: ToUpper, ToLower, Trim, Contains, HasPrefix, HasSuffix,
  Replace, Repeat, Split, Join. `conv`: Itoa, Atoi (returns
  `Result[int, string]` — errors are values even across the FFI).
  `math`: Sqrt, Pow, Floor, Ceil, Abs, Min, Max. `os`: Args, Getenv,
  Exit, ReadFile, WriteFile (Result-typed failures). `time`: Sleep
  (takes `duration` — a first-class type name), Unix, UnixMillis.
- `println`/`print` are prelude helpers, not Go's builtin println:
  real stdout and `%v` formatting (Go's builtin writes to stderr and
  prints floats as exponents).

## Scopes & names (§3)

- Shadowing: allowed across scopes; `:=` redeclaration within the same
  scope is an error (Go's rule).
- `_` is the blank identifier: assignable to, never readable.
- Namespaces: types and values share the file namespace; variant
  constructors live in a global constructor index — two enums exporting
  the same variant name make that name ambiguous and unusable unqualified.

## Packages & imports (§3)

- A package is a directory of `.gopp` files sharing one package clause
  and one namespace. Files merge in sorted order; diagnostics stay
  accurate via lexer line offsets against the concatenated source.
- `import "foo"` (or `"a/b"`, `".."` — relative directories only) loads
  that directory as a package. Imports come before declarations.
- The qualifier is the dependency's **package name** (Go's rule), not
  the path; two imports resolving to the same package name are an error
  (no aliases).
- Capitalized = exported; unexported names are invisible across packages.
- A local binding shadows a package qualifier (locals win, like Go).
- Import cycles are errors (`import cycle: a -> b -> a`); every package
  must live inside the root package's directory tree.
- Qualified use: `foo.Bar(...)`, `foo.Bar` (function value), `foo.Active`
  (unit variant), `foo.Failed("x")` (constructor), `foo.Box[int](v)`
  (generic constructor), `foo.Status` / `foo.Box[int]` (types).
- The prelude (`Result`, `Option`, `ms`/`second`/`minute`) is one shared
  package at both sema and Go level, so values crossing package
  boundaries keep type identity.

## Match (§9)

- Exhaustiveness: a match on an enum must cover every variant or have an
  unguarded catch-all (`_` or a bare binding). A match on a non-enum needs
  an unguarded catch-all.
- Guards never count toward coverage: a guarded arm may fail and fall
  through, so `Active if cond -> ...` does not cover `Active`.
- Usefulness: an arm shadowed by an earlier unguarded catch-all, or
  repeating an already-covered variant/literal, is an
  `unreachable pattern` warning.
- Guards on channel arms are rejected (Go channels can't peek).

## Flow (§9)

- All-paths-return: a function with results must return or diverge on
  every path. Divergence: `return`, `panic`, an exhaustive match whose
  arms all diverge, or `loop {}` / `for {}` with no break targeting it.
- Unreachable code after a diverging statement is a warning; the emitter
  drops it (Go demands functions *end* in a terminating statement).
- Warnings never block compilation.

## Removals from Go (§5)

These are language-level deletions, not lint rules — the names don't parse
or don't resolve, so the programs cannot be written:

- **`error` is not a type.** Failures are values: `Result[T, E]` from the
  prelude, propagated with `?`, handled with `match`. A function that can
  fail says so in its return type.
- **`any` is not a type.** It existed to punch holes in Go's type system;
  go++ has no hole to punch. Emitted Go still uses `any` inside generic
  instantiations — that's Go's encoding, not go++'s surface.
- **`<-` is not syntax.** Bare channel receive/send operators are gone;
  channels are used through `ch.recv()`, `ch.send(v)`, `ch.close()` (and
  `select`, which keeps its own syntax). One obvious way, no operator
  precedence puzzles.
- **No nil maps.** `var m map[K]V` emits `make(...)` — declared maps are
  always ready to write (the silent-crash footgun that motivated go++).
- **No implicit conversions.** `int32` and `int64` do not mix silently;
  conversions are explicit function-style calls (Phase B).

`null`/`nil` absence is handled by `Option[T]`; pointers (`&`/`*`) exist
but null pointer constants do not — there is no way to write one.

## Evaluation & runtime semantics

- Maps are instantiated on declaration: `var m map[K]V` emits `make(...)`.
  There are no nil maps in go++ (the silent-crash footgun that motivated
  the language).
- Overflow, division by zero, evaluation order: exactly Go's semantics —
  go++ emits Go and runs on its runtime.
- `break loop` targets the innermost `loop {}` block.

## Const evaluation (§10)

- `comptime expr` evaluates at compile time. Allowed inside: literals,
  the prelude duration constants (`ms`, `second`, `minute`), `true` /
  `false`, arithmetic / bit ops / shifts / comparison / logic, string
  concat, conversions, and nested `comptime`. Everything else —
  variables, function calls, channels, matches — is "not a constant
  expression".
- Integer math is exact (arbitrary precision, like Go constants);
  floats are float64 (a documented divergence from Go's
  arbitrary-precision constant floats).
- Errors are compile-time, with the runtime's own rules (§29):
  division/modulo by zero, overflow of a typed integer
  (`comptime int8(100) + int8(100)`), overflow of the default type
  (`comptime 1 << 100` overflows `int`), overflow against the declared
  type (`var x int8 = comptime 100 + 100`), shift counts above 4096,
  and the fuel limit — the compiler diagnoses, never hangs.
- The folded constant's type is the inner expression's type; untyped
  rules (§7) still apply at the use site. The emitter writes the
  constant, wrapped in a conversion when the type isn't the default
  (`int64(42)`, `time.Duration(60)`).
- Full comptime *functions* (compile-time execution of user code in
  function position) stay deferred — but see metaprogramming below.

## Comptime metaprogramming (§10)

- Top-level `comptime { ... }` blocks run during sema, BEFORE any name
  registration or type resolution: what they mutate is exactly what the
  rest of the pipeline (resolution, checking, exhaustiveness, codegen)
  sees. Blocks execute in source order; `gen` is visible to later blocks,
  and variables persist across blocks (later blocks use what earlier
  ones declared).
- Previously declared things are usable bare: `Color` evaluates to the
  enum's handle, `greet` to the function's, and a declared function can
  be CALLED at comptime — `n := fib(10)` interprets the body on the spot
  (locals, `if`, `for`, `loop`/`break`, `return`; fuel-bounded, call
  depth capped at 128). Runtime-only constructs (channels, match,
  println) are "not a comptime expression" there.
- `decls()` returns live handles to the package's declarations — not
  snapshots. Field access reads through, assignment writes through:
  `d.name = d.name + "bar"` renames the actual declaration.
- Handles: `FuncDecl` (.kind .name .params .results .body),
  `EnumDecl` (.kind .name .variants), `StructDecl` (.kind .name .fields),
  `Field` (.name .type), `Variant` (.name .fields). `.params`/`.results`/
  `.fields`/`.variants` are live lists with `.add(...)`; `.body` is the
  function's source text (read or replace).
- Constructors: `Param(name, type)` / `Field(name, type)` (type is source
  text or a type handle), `Variant(name)`, `Enum(name)`, `Struct(name)`,
  `Func(name)`; `gen(decl)` injects a built declaration into the package.
- Statement language: `for x in list { }` (for-in is comptime-only),
  `if/else`, `for`/`loop`/`break`, `:=` / `=` bindings, field assignment,
  `return` (inside comptime-called functions). Comptime `match` supports
  literal/wildcard/binding/bool arms with guards (expression bodies only);
  variant and channel patterns are comptime errors, and an unmatched
  subject is a compile error ("non-exhaustive comptime match"), not a
  panic.
- Expressions: literals, arithmetic/logic/string ops, field access,
  indexing, and the builtins `print` (to stderr, like Zig's @compileLog),
  `len`, `str`, `decls`, `gen`, the constructors, and the string tools
  `split` / `join` / `upper` / `lower` / `trim` / `replace` / `contains` /
  `has_prefix` / `has_suffix` / `repeat` (count ≤ 10000) for codegen.
- Sharp edges, on purpose: renaming a type does not rewrite references
  to it; metaprogramming errors are ordinary diagnostics; fuel-bounded.

## Memory model

Garbage-collected via the Go runtime. §20-§21 (ownership, moves, borrows,
drop order) do not apply and are deliberately deleted from the roadmap.

## Deliberately deferred, with reasons

- **§17 identifier interning** — pure performance; at ~4k LOC the win is
  unmeasurable against the churn. Revisit when compile times hurt.
- **§8 behaviors** — LANDED: behavior/impl/bounds, method resolution,
  coherence. Remaining: impls on generic types, cross-package impls,
  default bodies, multi-bounds.
- **§14 operator overloading** — LANDED for the arithmetic/comparison
  core; compound assignment, indexing, and shifts on user types remain.
- **§16 macros** — no macro syntax; top-level comptime blocks (§10
  metaprogramming) cover the code-generation-shaped wants with live AST
  handles instead of token rewriting.
- **§19 glob imports, §25 effects** — no syntax for them.
- **§27 incremental** — the pass architecture and side tables were built
  so queries wrap around, not rewrite in. Later.
- **§28 LSP** — v1 landed (`gopp lsp`): diagnostics, hover, definition,
  completion, document symbols. Single-file analysis; local-var
  definitions and import-aware analysis are the known gaps.

## Testing (§12)

- `tests/ui/*.gopp`: `//~ ERROR msg` / `//~ WARN msg` annotations matched
  against actual diagnostics, both directions (missing and unexpected
  diagnostics both fail).
- `TestFuzzNoCrash`: random byte soup + mutated valid programs through
  the full pipeline; the compiler must diagnose, never panic.
- `e2e_test.go`: compile and run the examples, assert exact output.
