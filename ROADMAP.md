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
| 5 | coherence + orphan rules | ➖ no behaviors/impls |
| 6 | module tree + lexical scope stack | ⚠️ scope stack ✅; no modules (single file) |
| 7 | namespaces (Type/Value/Behavior) | ⚠️ Type+Value; no Behavior namespace |
| 8 | multi-segment paths, per-hop visibility | ➖ no modules |
| 9 | import fixpoint | ➖ no imports |
| 10 | shadowing policy enforcement point | ✅ same-scope `:=` = error, cross-scope allowed |
| 11 | private-type leak check | ➖ no visibility |
| 12 | interned nominal types, alias-vs-newtype | ⚠️ nominal ✅; interned ❌; no aliases in language |
| 13 | Error poison, Never bottom | ✅ |
| 14 | kind/arity checking | ✅ |
| 15 | bidirectional check/infer, literal defaulting, blame boundaries | ✅ + untyped constants w/ overflow checks, explicit conversions |
| 16 | unification: occurs, fuel, invariance, no implicit conv | ⚠️ no-implicit-conv ✅; ctor inference is §8-lite pattern matching, no engine (by design) |
| 17 | generics checked once against bounds | ⚠️ ctor type-arg inference from context + value args ✅; no generic functions |
| 18 | behavior resolution, deferred obligations | ➖ no behaviors |
| 19 | method lookup order + ambiguity | ⚠️ hardcoded methods (chan, Result); variant ambiguity ✅ |
| 20 | closure capture analysis | ➖ no closures |
| 21 | monomorphization set collection | ➖ emitter generates generic Go |
| 22 | value-restriction sidestep | ➖ no local generalization at all |
| 23 | exhaustiveness + usefulness, guards excluded | ✅ (flat patterns — Maranget unneeded until nesting) |
| 24 | definite initialization | ⚠️ by design: Go zero values + maps auto-init (SPEC.md) |
| 25 | all-paths-return, unreachable code | ✅ |
| 26 | const eval + fuel | ➖ no `const` decls |
| 27 | diagnostics: spans, secondary labels, suggestions | ⚠️ line numbers ✅; labels/suggestions ❌ |
| 28 | test harness `//~ ERROR`, snapshots, fuzzing | ✅ annotations both directions + fuzz |

**Score: 10 full ✅ · 9 partial ⚠️ · 9 N/A by language scope · 0 unaddressed**

Language-level wins beyond the skeleton (the "better types" program):
removals of `error` / `any` / `<-` / nil maps · untyped literals with
compile-time overflow checks · explicit-only numeric conversions ·
generic constructor inference (`var r Result[int, string] = Ok(1)`,
`var o Option[int] = None`).

## EXTENDED §14-§28 — all deferred except:

| § | item | status |
|---|------|--------|
| 17 | identifier interning | ❌ deferred (perf-only at this size) |
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

1. ~~Parser recovery~~ ✅ · ~~Structs~~ ✅ · ~~`?` try~~ ✅ ·
   ~~better types (removals, widths, conversions, ctor inference)~~ ✅
2. **`comptime` expr** — const eval with fuel (§10); the checker already
   range-checks literals, this generalizes it to expressions.
3. **Imports / modules** — reactivates §3 module tree, §8 paths, §9
   import fixpoint.
4. **Diagnostics polish** (§11) — column spans, secondary labels
   ("expected because of this"), more suggestions.
5. **§17 interning + §1 integer IDs** — when compile times or LSP make
   them pay; both are wrappers, not rewrites, by design.

Cancelled: `@derive` (superseded by the better-types program — the
language fixes the types instead of generating boilerplate over weak
ones). Full §8 (generic functions, unification, behaviors) stays
deferred per SPEC.md.
