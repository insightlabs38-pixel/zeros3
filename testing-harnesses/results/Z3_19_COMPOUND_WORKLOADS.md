# Z3-19 — Compound / nested representation workloads (decision record)

Laboratory result; no production file changed (`zeros3.go`, root `go.mod`, manifests, journals, pack/store formats, read paths, GC, snapshots, registry client).
Code: `z3/composez3` (coordinator), `z3/cmd/z3compose`, small additions to `lab` (`Piece.Derived`, `Comp.Pad/Pre`), `gitz3.RepOpts.WholeMin`, `srev.Member.Derived`.
No representation contract is frozen here.

**Question.** Can exact representations compose (outer → exposed payload → inner → SHA-256 components) without losing exactness, double-charging,
exploding metadata/reads, or special-casing every level?

**Conclusion.** Yes, and the composition itself is tiny: a ~50-byte record per inner object, an inner recipe stored as an ordinary component, and one new
flag (`Derived`: named and verified by the outer recipe, bytes produced by the inner one). It pays where the inner transform finds reuse the outer
cannot expose: SQLite in a layer 8.4× cheaper than CDC on a VACUUM revision (12.6× on 100 sparse updates), a reordered CAR in an archive ~2,000×, Go executables of adjacent
releases −20 %. It does **not** pay by itself: the first revision costs 0–5 % more, unrelated executables +4 %, and Git's own delta beats the inner transform on executables.
Savings are *complementary*, not additive: tar+gzip with whole members loses to CDC in every A/B/D row and wins only with the inner transform.

## Model and exactness

`Compose(outerPlan)`: member payloads (`Class tarpay`, or a whole Git blob `gitobj`) ≥ 64 KiB whose first bytes match ELF/PE, SQLite or CAR get an inner
representation; the payload piece keeps its digest and length in the outer recipe but is not stored. Record: `CMP1 | depth | n × (piece index, kind, prefix, pad,
inner-recipe SHA-256)`. Rebuild uses only the serialised record, the round-tripped outer piece list and stored components: inner recipe (a component, digest-checked)
→ inner rebuild → prefix + object + padding → SHA-256 must equal the outer digest → outer rebuild (gzip IR replay / tar join / Git pack). Depth is a constant
(outer 1, inner 2); a third level is refused (`ErrDepth`, tested) and nothing scans recursively. Detection is a magic sniff of one member. An inner failure or non-match
leaves the member to the outer representation (a fake ELF in the test layer falls back; the layer stays exact). Every composed row of the shared runner is serialised,
deserialised, rebuilt from stored components and compared by length, SHA-256 and bytes: **0 failures** in every row (A 3 states × 7 representations, B 4 × 7, D 2 × 4 × 7, C 2 × 4 × 5).
Tests: record truncation/mutation, tampered and missing inner components, hostile ELF directory (every truncation, every mutation, 20 s fuzz smoke), depth bound.
Duplicate charging: `Audit` (derived payload also stored, or one ID stored under two roles) found 0 cases; a derived piece is charged its reference, never its bytes.
The outer gzip-IR skeleton and Git meta still live in the existing rebuild closures (charged byte for byte, as in Z3-16/17): portability debt, not new state.

## Corpora (all four workloads: CDC / outer / inner / composed, production pack rule, cost of each state added to a store holding the earlier ones)

A real: node-exporter v1.7.0 / v1.8.0 / v1.8.1, the amd64 layer holding one 19–20 MB Go ELF (existing pinned `z3oci fetch`, succeeded first try, 67 MB cached outside Git).
B synthetic: layer = config + 1 MiB library + SQLite DB (Z3-18 revisions r0, 100 sparse updates, VACUUM, 1000 updates). D synthetic: tar.gz of a 10 MiB CAR (`v1-reorder`,
`v1-add`). C: Git (2.43.0, SHA-1) histories of 4 Go builds of `cmd/z3lab` (`z3bin corpus`, 6.3 MB each) and 4 revisions of an app SQLite database (6.2 MB).

| workload / state | CDC | outer tar+gzip | outer-cdc | **composed** | inner alone (loose) | winner |
|---|---:|---:|---:|---:|---:|---|
| A v1.7.0 (first sight) | 10.0M | 10.3M | 10.4M | 10.4M | 10.2M | CDC |
| A v1.8.0 (unrelated binary) | 10.3M | 10.6M | 10.7M | 10.7M | 10.5M | CDC |
| A v1.8.1 (patch release) | 10.3M | 10.6M | 9978K | **8373K** | 8188K | outer+inner |
| B r0 / VACUUM / 100 sparse / 1000 updates | 12.9M / 11.8M / 8062K / 8158K | 13.5M / 12.4M / 12.2M / 12.5M | 13.6M / 11.2M / 3348K / 8426K | 13.6M / **1431K** / **641K** / **2610K** | 11.9M / 755K / 309K / 1917K | outer+inner |
| D reorder r1–r3 | 10.3M / 10.1M / 10173K | 10.5M ×3 | 7893K / 7611K / 7435K | **5.3K ×3** | 4.5K ×3 | outer+inner |
| D add blocks r1 / r2 / r3 | 288K / 10185K / 10.5M | 10.6M / 10.9M / 11.0M | 143K / 758K / 218K | **127K / 320K / 117K** | 126K / 319K / 116K | outer+inner |
| C ELF commits 2–4 (Git delta pack / composed) | 2054K / 2018K / 1875K | git-aware(Δ) **1793K / 1822K / 1696K** | no-Δ 3094K / 3165K / 3104K | 2280K / 2293K / 1999K | 2232K / 2236K / 1936K | **Git-aware (outer only)** |
| C SQLite commits 2–4 | 6175K / 6235K / 6358K | git-aware(Δ) 3485K / 3398K / 3485K | no-Δ 3469K / 3401K / 3503K | **834K / 831K / 915K** | 696K / 682K / 750K | outer+inner |

Chain totals (cumulative): A CDC 32.07M, outer 33.08M, **composed 30.64M (−4.4 %)**; B CDC 42.4M, outer 53.1M, outer-cdc 38.0M, **composed 19.0M**; D reorder CDC 42.8M → **11.0M**,
D add 32.7M → **11.6M**; C ELF Git-aware(Δ) **9.27M** vs composed 10.57M vs CDC 15.5M (no-Δ); C SQLite Git-aware(Δ) 17.0M vs **composed 9.0M**. In C rows the columns are: CDC of the delta pack; Git-aware recipe of the delta pack; Git-aware recipe of the no-delta pack; no-delta pack + inner.

## Accounting of compound rows (bytes; payload+component | outer recipe | inner recipe | index/record | total = charged)

A v1.7.0 9,891,974 | 65,340 | 927,784 | 43 | 10,885,141; A v1.8.1 7,577,599 | 67,054 | 929,151 | 43 | 8,573,847. B VACUUM 1,237,990 | 64,086 | 162,931 | 42 | 1,465,049.
D reorder r1 0 | 851 | 4,572 | 43 | 5,466. C SQLite commit 2 771,137 | 30,205 | 52,294 | 105 | 853,741. Every row sums exactly to its total. The inner recipe class is the whole
premium: ELF reference metadata is ~0.93 MB (4.7 % of the binary) and is **not** reused between releases, SQLite nodes 0.16 MB (0.8 %), CAR 4.6 KB.

## Per-object plans, additivity, negatives

- **Plans.** outer+inner < both: A v1.8.1, B ×3, D ×6, C SQLite ×3. CDC wins: A v1.7.0, A v1.8.0. Outer-only wins (Git delta): C ELF ×3. Inner-alone cannot win a layer
  (it needs the payload exposed); on loose artifacts it beats CDC 1.2–1,700×.
- **Relation.** Complementary, not additive: outer-alone (whole members) loses to CDC in all A/B/D rows (+2 % to +51 %) while inner-alone gains; composed gains exceed both (B m02: outer −4.5M,
  inner +2.7M, composed +7.4M vs CDC). Outer with CDC inside members does win in B/D but never approaches the composed cost. Overlap/destruction appear only in C: Git's delta and the inner transform capture the same ELF redundancy (composed costs 1.2–1.3× Git-aware(Δ)).
- **Greedy oracle** (cheapest plan under the current store) adopts the transform only when the first revision is no dearer than CDC (D); in A and B it stays at CDC (A 32.07M, B 42.4M): no plaintext is stored to reuse.
  The implementable heuristic *admit when the first-sight cost is within +10 % of CDC, or cheaper now* adopts in every row above and gets A −4.4 %, B −55 %, D −65…−74 %.
  It would also adopt for an unrelated sequence and lose 4 % per object (A v1.7.0→v1.8.0): relatedness is still unknown at first sight.
- **Negatives:** unrelated Go releases (+0.4M per layer), Git delta on executables (above), tar+gzip alone, and a 5 % inner-metadata premium that never amortizes without reuse.

## Reads and execution (composed winners; `reads` in each run)

| winner | whole rebuild | one inner object | range | analysis / rebuild | peak heap (rebuild) |
|---|---|---|---|---|---|
| B SQLite layer (12.9 MB gz) | 5,794 comps, 20.8 MB | 4,943 comps, 33 ms | **one 4 KiB page: 2 comps, 6.2 KB** (CDC: the gzip stream, 12.9 MB) | 7 / 6 MB/s | +236 MiB |
| A node-exporter layer (10.8 MB gz) | 840 comps, 22.8 MB | 278 comps, 100 ms | — | 9 / 5 MB/s | +173 MiB |
| D CAR archive (11.0 MB gz) | 126 comps, 11.0 MB | 123 comps, 20 ms | — | 137 / 204 MB/s | +112 MiB |

Outer gzip-IR replay (5–6 MB/s on A/B) dominates time; one inner object rebuilds at 230–560 MB/s. Whole-layer rebuilds hold payload and output in memory (the outer interface asks for whole components): +173–237 MiB.
Reading one member needs no gzip replay; whole-layer reads touch 1.7–2.1× the layer's bytes (plaintext components, before pack compression) and 5–30× the components of CDC.

## Primitive review

1. **Recurring across workloads:** a member named and digest-checked by the outer recipe but produced by an inner one (`Derived`, 4/4); an inner recipe that is itself a stored component reached
   through a record (`Via`, 4/4); framing around the nested object (tar padding in A/B/D, Git header in C); per-level exact fallback; first-sight premium vs reuse (admission, 4/4); class-split accounting.
2. **Format-specific:** reference slots and units (ELF/PE), page head split and node layout (SQLite), CID derivation and block order (CAR), Git entry modes and delta programs, gzip skeletons.
3. `srev.Revision` still expresses members, `Via`, residual, index and rebuild. It lacked one concept needed by all four workloads, so `Member.Derived` was added (nothing else was).
4. **Still lab accounting, not a serialised concept:** `Residual`/`Index` as counts (the outer gzip/Git skeletons sit in closures; only the ~50-byte record is real bytes), component classes, `Comp.Pad/Pre` hints,
   the 96 B/ref model, whole-component `get`, and the dedupe store standing in for GC reachability.
5. **For the contract batch:** a typed, versioned recipe object stored as a component; member roles stored / via / derived with framing; bounded nested decode over ReaderAt/Writer with ranged component reads;
   reachability for payloads held only by inner recipes (GC); per-level verification and fallback semantics; a per-component store-overhead term; and an admission rule that prices relatedness.

## Limitations and carry-forward

One real corpus (3 releases); B, C, D are locally produced. Component-count overhead and GC reachability are not modelled. Only ELF, SQLite and CAR as inner kinds; no depth-3 case; PE is untested here.
Carry-forward: a ranged `get` and streaming outer rebuild; replacing closure-held skeletons by stored recipe objects; Safetensors needs base-relative inner objects (base reachability) and was not composed.

## Reproduce

```sh
cd z3 && go run ./cmd/z3oci fetch -group nodeexp && go run ./cmd/z3bin corpus -out "$D/z3bin" -goos linux -flags std     # caches outside Git
go run ./cmd/z3compose oci | sqlite | car      # Z3_STRUCT_CACHE from Z3-18
go run ./cmd/z3compose git -bin "$D/z3bin/linux-amd64/std"; go test ./composez3 ./gitz3
```
