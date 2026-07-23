# go++ global tracking — skeleton coverage & roadmap

Status vs. the ZEN SEMA SKELETON coverage map. Legend:
✅ landed · ⚠️ partial / diverges by design · ➖ N/A (language has no such
feature; documented in SPEC.md) · ❌ not done

## CORE §0-§12 (v1 scope) — 28 items

| # | item | status |
|---|------|--------|
| 1 | pass architecture, side tables, partial results | ✅ parser does panic-mode recovery; sema recovers fully |
| 2 | stable index-based IDs | ⚠️ side tables keyed by AST pointers, not integer IDs |
| 3 | two-phase collection, forward refs, mutual recursion | ✅ |
| 4 | infinite-size type cycle detection | ✅ structs, deterministic DFS |
| 5 | coherence + orphan rules | ✅ one impl per (behavior,type), one method name per type, local-only targets |
| 6 | module tree + lexical scope stack | ✅ directory packages, merged namespaces, scope stack |
| 7 | namespaces (Type/Value/Behavior) | ✅ behaviors registered in their own table |
| 8 | multi-segment paths, per-hop visibility | ⚠️ two-segment `pkg.Name`, exported rule per hop; no deeper paths |
| 9 | import fixpoint | ✅ recursive loader + memo; cycles error, no fixpoint needed (imports resolve before sema) |
| 10 | shadowing policy enforcement point | ✅ same-scope `:=` = error, cross-scope allowed; locals shadow qualifiers |
| 11 | private-type leak check | ⚠️ exported rule enforced at use sites; no public-signature leak check yet |
| 12 | interned nominal types, alias-vs-newtype | ⚠️ nominal ✅; interned ❌; no aliases in language |
| 13 | Error poison, Never bottom | ✅ |
| 14 | kind/arity checking | ✅ |
| 15 | bidirectional check/infer, literal defaulting, blame boundaries | ✅ + untyped constants w/ overflow checks, explicit conversions |
| 16 | unification: occurs, fuel, invariance, no implicit conv | ⚠️ no-implicit-conv ✅; ctor inference is §8-lite pattern matching, no engine (by design) |
| 17 | generics checked once against bounds | ✅ rigid params + behavior bounds, checked once; call sites verify impls |
| 18 | behavior resolution, deferred obligations | ⚠️ resolution + bounds ✅; no deferral loop (no inference vars) |
| 19 | method lookup order + ambiguity | ⚠️ fields > hardcoded (chan, Result) > behavior impls; coherence kills ambiguity |
| 20 | closure capture analysis | ➖ no closures |
| 21 | monomorphization set collection | ➖ emitter generates generic Go |
| 22 | value-restriction sidestep | ➖ no local generalization at all |
| 23 | exhaustiveness + usefulness, guards excluded | ✅ (flat patterns — Maranget unneeded until nesting) |
| 24 | definite initialization | ⚠️ by design: Go zero values + maps auto-init (SPEC.md) |
| 25 | all-paths-return, unreachable code | ✅ |
| 26 | const eval + fuel | ✅ `comptime expr` (exact big.Int math, overflow/div-zero are compile errors, fuel) + top-level comptime metaprogramming: live AST handles, walk/mutate/`gen` declarations, for-in, print |
| 27 | diagnostics: spans, secondary labels, suggestions | ✅ snippets+carets on exprs/stmts/patterns/types, notes (return blame, redecl), did-you-mean; decl-level errors stay line-only |
| 28 | test harness `//~ ERROR`, snapshots, fuzzing | ✅ annotations both directions + fuzz |

**Score: 17 full ✅ · 8 partial ⚠️ · 3 N/A by language scope · 0 unaddressed**

Language-level wins beyond the skeleton (the "better types" program):
removals of `error` / `any` / `<-` / nil maps · untyped literals with
compile-time overflow checks · explicit-only numeric conversions ·
generic constructor inference (`var r Result[int, string] = Ok(1)`,
`var o Option[int] = None`).

## EXTENDED §14-§28 — all deferred except:

| § | item | status |
|---|------|--------|
| 14 | operator overloading | ⚠️ arith/comparison/unary core ✅; no compound-assign/index/shift overloads |
| 17 | identifier interning | ❌ deferred (perf-only at this size) |
| 28 | LSP | ⚠️ v2: + local-var defs, import-aware analysis, qualified completion; no dirty-buffer deps |
| 29 | spec decisions written down | ✅ SPEC.md |

## Meta-knowledge checklist (theory landmines)

✅ error poisoning · ✅ blame boundaries via bidirectional · ✅ guards
excluded from exhaustiveness (undecidable otherwise) · ✅ no subtyping →
no variance bugs · ✅ arity-as-kinds · ✅ invariance (explicit enum
instantiation only) · ✅ fuzz early · ✅ spec written down · ✅ isorecursive
nominal types by construction · ➖ occurs check / fuel / value restriction /
coherence — all arrive with §8, documented in SPEC.md · ✅ "compilers run
on broken code 95% of the time" — parser (panic-mode) and sema both
recover · ✅ conflicts poison inference (one mistake, one diagnostic).

## Next up, in dependency order

- ~~generic structs~~ ✅ · ~~chan<T> syntax~~ ✅ · ~~[T] slice syntax~~ ✅ · ~~multiple results~~ ✅ · ~~actors (Pony)~~ ✅ · ~~goplex~~ ✅ · ~~goparse~~ ✅ · ~~gocheck (sema-lite)~~ ✅ · ~~SELF-HOSTED EMIT (m4)~~ ✅

1. ~~Parser recovery~~ ✅ · ~~Structs~~ ✅ · ~~`?` try~~ ✅ ·
   ~~better types~~ ✅ · ~~`comptime` expr (§10)~~ ✅ ·
   ~~diagnostics polish (§11)~~ ✅ · ~~imports / modules (§3)~~ ✅ ·
   ~~comptime metaprogramming (§10)~~ ✅ · ~~sema columns (§11)~~ ✅ ·
   ~~LSP v1 (§28)~~ ✅ · ~~CI/README/`gopp run`~~ ✅ ·
   ~~comptime match + string builtins~~ ✅
2. ~~stdlib v1 (str, conv + native FFI)~~ ✅ · ~~stdlib v2 (math, os, time)~~ ✅ · ~~generic impls~~ ✅ ·
   ~~compound assign (+ sema leak fix)~~ ✅
3. ~~default method bodies (§23-lite)~~ ✅
4. ~~indexing overloads (`index`/`set` methods)~~ ✅
5. ~~LSP v2~~ ✅; **§17 interning** next optional; cross-package
   impls: DECLINED (Go methods must live in the type's package — SPEC.md).
3. **§17 interning + §1 integer IDs** — when compile times or LSP make
   them pay; both are wrappers, not rewrites, by design.
4. **LSP v2** — import-aware analysis, local-var definitions, column
   positions end-to-end.

Cancelled: `@derive` (superseded by the better-types program — the
language fixes the types instead of generating boilerplate over weak
ones). §8 and §14 landed.
