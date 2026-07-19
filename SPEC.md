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
  int64 count; `d * 3` must stay convenient).
- **No implicit conversions between typed values.** `int8 + int64` and
  `int32 < int64` are "mismatched types" errors. The escape hatch is an
  explicit conversion, `int64(x)` — a basic type name in call position.
  Numeric↔numeric converts freely; `rune`↔`string` converts;
  `string(int)` is rejected (did you mean `string(rune(...))`?);
  everything else is a "cannot convert" error. Identity conversions are
  allowed.
- Inference is function-local and annotation-free: `:=` takes the RHS
  type directly (defaulting untyped literals). There are no inference
  variables, hence no unification engine and no occurs check — they
  arrive with generic functions (§8), which are deliberately not built
  yet. Constructor type-argument inference (below) is pattern matching,
  not unification.

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

## Scopes & names (§3)

- Shadowing: allowed across scopes; `:=` redeclaration within the same
  scope is an error (Go's rule).
- `_` is the blank identifier: assignable to, never readable.
- Namespaces: types and values share the file namespace; variant
  constructors live in a global constructor index — two enums exporting
  the same variant name make that name ambiguous and unusable unqualified.

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
  go++ emits Go and runs on its runtime. No const evaluation exists yet
  because the language has no `const` declarations (§10 deferred).
- `break loop` targets the innermost `loop {}` block.

## Memory model

Garbage-collected via the Go runtime. §20-§21 (ownership, moves, borrows,
drop order) do not apply and are deliberately deleted from the roadmap.

## Deliberately deferred, with reasons

- **§17 identifier interning** — pure performance; at ~4k LOC the win is
  unmeasurable against the churn. Revisit when compile times hurt.
- **§8 generic functions + behaviors** — the skeleton's own rule: the
  monomorphic language checks end to end first. This brings unification,
  deferred obligations, and the occurs check.
- **§14 operator overloading** — needs behaviors first.
- **§10 const eval** — no `const` declarations exist; arrives with them.
- **§16 macros, §19 glob imports, §25 effects** — no syntax for them.
- **§27/§28 incremental + LSP** — the pass architecture and side tables
  were built so these wrap around, not rewrite in. Later.

## Testing (§12)

- `tests/ui/*.gopp`: `//~ ERROR msg` / `//~ WARN msg` annotations matched
  against actual diagnostics, both directions (missing and unexpected
  diagnostics both fail).
- `TestFuzzNoCrash`: random byte soup + mutated valid programs through
  the full pipeline; the compiler must diagnose, never panic.
- `e2e_test.go`: compile and run the examples, assert exact output.
