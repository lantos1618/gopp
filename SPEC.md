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
  errors. There is no truthiness; conditions must be `bool`.
- Aliases: none in the language. Generics: enum declarations only
  (`Result[T, E]`), instantiated explicitly at use sites
  (`Ok[int, string](1)`). Arity is checked (§5).

## Inference & literals (§6, §7)

- Bidirectional checking: expressions infer bottom-up; declarations,
  return statements, call arguments, and match-in-value-context are
  CHECK mode — the expected type propagates downward. Signatures and
  declarations are the blame boundaries.
- Literal defaulting: an integer literal checked against a numeric type
  adopts that type (`var x int64 = 5`); unconstrained it defaults to `int`.
  Float literals default to `float64`.
- Inference is function-local and annotation-free: `:=` takes the RHS
  type directly. There are no inference variables, hence no unification
  engine and no occurs check — they arrive with generic functions (§8),
  which are deliberately not built yet.

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
