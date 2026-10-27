# Z3-32 — Managed bytecode indexed-reference frontier (DEX/APK, JVM class/JAR)
Laboratory only; production files untouched (diff empty). Code: `z3/dexz3`, `z3/jvmz3`, `z3/idxref` (shared), `z3/pdelta` (Z3-12 delta, moved), `z3/repz3/kinds/dex.go` (lab `KindDEX` = 11), `z3/cmd/z3mb`. Sizes: packed MiB (M) / KiB (K) added to a store holding the earlier revisions; production pack rule, 96 B/ref, index nodes of ~64 groups.
**Question → answer.** Can managed bytecode be stored as stable content-addressed code/data plus exact symbolic-index recipes? **DEX: yes** — 96–99 % of the "changed" DEX bytes are index/offset renumbering (true literal 0–1.2 %); a masked payload plus 3-byte semantic-identity references rebuilds every DEX exactly and cuts the real 5-APK history 45.6 → 27.5 M (−40 %; patch releases 4–9×) at +14–20 % first sight. **JVM: no** — whole-class identity + exact DEFLATE already holds the reuse; normalization loses on all three real JAR families.
**Decisions.** DEX indexed-reference representation `PROMOTE_TO_I2` · APK composition `DEFER` (needs a durable exact-ZIP kind, I1) · JVM class representation `KILL` · JAR composition `KILL` (inner transform) · `INDEX_SUBSTRATE_PARTIAL` · **MANAGED_BINARY_WAVE_READY**.
## 1. Corpus (hash-pinned or provenance-recorded; committed: two 15 KB DEX and 13 class fixtures with their sources)
| class | artifact | producer / provenance |
|---|---|---|
| REAL | F-Droid client 2.0-rc0, rc1, 2.0.0, 2.0.1, 2.0.2 APKs (12.5–13.3 MB, 3 × `classes*.dex` DEX 037, 9.3+9.6+1.7 MB; 15 DEX = 98 MiB) | `realworld/sources.json`, GPL-3.0+, R8-built by F-Droid |
| REAL | Guava 31.1…33.0.0 (5), Commons Lang 3.11…3.14 (4), JUnit 4.11…4.13.2 (5) JARs: 13 136 class files (40 MiB) over 14 JARs | Maven Central, `.sha1` verified, Apache-2.0 / EPL-1.0 |
| PRODUCER_WORKLOAD | generated Java library, 800 classes, 11 one-mutation variants + 7-step chain (1.9 MB DEX, 1.35 MB JAR) | OpenJDK 21.0.12 `javac --release 8`; D8 8.7.18 `--release --min-api 24` (sha256 58366f77…); `jar --date=2021-01-01` |
| SYNTHETIC_FIXTURE | 12 APK-shaped ZIPs (metadata, reorder, stored DEX, extra asset, resource-only, multidex, wholesale) | Info-ZIP 3.0, unsigned; layout controls, no Android semantics |
## 2. Where the changed bytes are (`z3mb attrib`; PAIRWISE SENSITIVITY: delta literal bytes of the new file vs the old, split by what the byte is; % of raw literal)
| format · pair | raw literal (% file) | true literal | index refs | offset/layout | debug/metadata | residual (unchanged bytes dragged) |
|---|--:|--:|--:|--:|--:|--:|
| DEX F-Droid rc0→rc1 | 66.3 | 1.2 | 32.3 | 14.2 | 2.1 | 50.1 |
| DEX F-Droid 2.0.0→2.0.1 | 28.5 | 0.1 | 36.0 | 24.5 | 2.9 | 36.6 |
| DEX controlled one body change | 14.6 | 0.1 | 37.1 | 33.2 | 4.7 | 24.9 |
| JVM Guava 31.1→32.0.0 / 32.1.3→33.0.0 | 25.3 / 4.8 | 17.4 / 10.9 | 31.2 / 31.8 | – | 39.5 / 42.3 | 12.0 / 15.1 |
| JVM Commons 3.13→3.14 / JUnit 4.12→4.13 | 56.0 / 32.7 | 31.5 / 55.5 | 19.3 / 15.8 | – | 27.3 / 18.3 | 22.0 / 10.4 |
Slots are 32 % of a real DEX (ID-table indexes 11, operands 8, class_data deltas 1.4, type lists 0.9, annotations 0.6, offsets 9.6 %); masking all of them takes the pairwise literal from 28–66 % to 0.4–2.1 % (one changed body: **426 B**). Production CDC stays ≈100 % new (70.6 % for 2.0.1) and 65–81 % new even when masked (16–256 KiB chunks vs scattered edits): granularity, not coverage, binds. JVM classes: 15 % slot bytes, churn mostly real (LineNumber/LocalVariable/StackMap tables) and **no changed class differs only by renumbering**.
## 3. DEX / APK exact representation
Masked payload (header and map kept exact; every index/offset slot overwritten, ULEB lengths preserved) in record-aligned content-defined groups (4 KiB mean), each followed by its reference bytes: five per-space streams of `SHA-256(semantic identity)` prefixes (K0 = 3 bytes + implied disambiguation; string = MUTF-8 bytes, type = descriptor, proto = ret + params, field/method = class + name + type/proto) and one offset stream (14 offset kinds: first reference = ordinal delta, shared item = content class + occurrence). Decoder = `dexz3.Decode(meta, groups)`: re-walks the masked bytes, resolves offsets, then string < type < proto < field/method; adler32 re-verified. Branch offsets stay raw (method-local). Debug/annotation slots are walked (real D8/R8 builds have no local-variable slots).
Evidence and safety: every represented object is encoded, serialized, the analysis state dropped and decoded from `Meta` + group bytes (length, SHA-256, bytes; the encoder runs this itself and falls back to CDC on any mismatch): **34 DEX (98 MiB real + 34 MiB controlled), 28 522 class files, 18 APK/JAR containers, 0 fallbacks, 0 mismatches**. Non-canonical ULEB needs no producer predictor (the mask carries the length; padded deltas are tested). Ambiguous identities (duplicate strings, methods) make the DEX a CDC fallback; JVM duplicates resolve through occurrence numbers. Tests: table-driven parser/instruction/payload/jumbo/ULEB/malformed/hostile/tamper suites, a test-only DEX writer, fixtures from D8/javac, ZIP composition cases (DEFLATE, stored, reorder, opaque signing block, multi-member, corrupt members), `KindDEX` through `repz3.Prove`, two 25 s fuzz runs (191 k and 2.0 M executions).
| workload (new packed) | production CDC | structural (best of sections / 4K units) | index-normalized (prod CDC) | normalized + grouped (4K) | APK: outer → outer+DEX |
|---|--:|--:|--:|--:|--:|
| first sight, controlled | 518 K | 511 K | 438 K | 632 K | 1.35 M → 1.46 M |
| body change / const change | 521 K / 506 K | 508 K / 504 K | 308 K / 342 K | **26 K / 9.7 K** | 542 K → 48 K |
| add method / add class | 489 K / 538 K | 508 K / 512 K | 332 K / 399 K | **18 K / 31 K** | 510 → 38 K / 559 → 52 K |
| insert early string / early class (index shift) | 505 K / 536 K | 511 K / 512 K | 330 K / 362 K | **18 K / 23 K** | 557 K → 44 K |
| dependency addition (+60 classes) | 524 K | 516 K | 364 K | 86 K | 545 K → 107 K |
| wholesale body rewrite / unrelated APK | 515 K | 505 K | 429 K | 560 K (+9 %) | 537 → 582 K · APK 1.35 → 1.46 M |
| resource-only APK / ZIP metadata / member reorder | – | – | – | – | 428 → 429 K · 8.0 → 9.1 K · 13 → 14 K |
| multidex split (1 → 2 DEX) / stored DEX | 518 K | – | – | – | 558 → 282 K · 9.6 → 8.3 K |
| **real DEX/APK**: first sight rc0 | 8.42 M | 8.37 M | 8.52 M | 10.1 M (+20 %) | 11.5 → 13.1 M (+14 %) |
| rc1 / 2.0.0 | 8.40 / 8.46 M | 8.39 / 8.45 M | 7.06 / 7.00 M | 5.03 / 3.80 M | 9.62 → 6.25 · 9.54 → 4.87 M |
| 2.0.1 / 2.0.2 (patch releases) | 5.31 / 7.86 M | 4.68 / 7.72 M | 3.33 / 4.42 M | **0.59 / 0.87 M** | 6.16 → 1.43 · 8.86 → 1.86 M |
| **history total** (APK-level CDC 46.3 M) | 38.4 M | 37.6 M | 30.3 M | **20.3 M** | 45.6 → **27.5 M** |
Sweep (real, total / first sight): 64 K groups 30.3 / 8.5 M · 16 K 23.5 / 9.2 · 8 K 21.9 / 9.6 · **4 K 20.3 / 10.1** · 2 K 19.9 / 10.9 · per code item 36.8 / 17.9 M (121 935 objects). Controlled insert-symbol: 1 K 30 K · 4 K 23 K · 16 K 75 K · 64 K 162 K · per item 140 K. 32 B/ref moves totals < 1 %; flat 96 B refs without index nodes cost 22.0 M. K0 = 2 loses (42.0 vs 33.5 M star total: collision churn), K0 = 4 ≈ K0 = 3.
## 4. JVM class / JAR (`outer` = ZIP framing + exact DEFLATE + whole-class identity)
| workload (new packed) | production CDC | ZIP/class identity (outer) | + CP-normalized (whole class) | + normalized, grouped (CP | body) | standalone classes: identity → CP+body |
|---|--:|--:|--:|--:|--:|
| Guava: 31.1 / 32.0.0 / 32.1.0 | 2.64 / 2.71 / 2.49 M | 2.86 / 2.88 / 1.02 M | 3.21 / 3.23 / 1.12 M | 3.35 / 3.34 / 0.90 M | 2.63→3.12 · 2.64→3.11 · 0.81→0.67 M |
| Guava 32.1.3 / 33.0.0 / **total** | 2.00 / 2.49 / 12.3 M | 0.78 / 1.21 / **8.73 M** | 0.85 / 1.32 / 9.72 M | 0.72 / 1.14 / 9.42 M | 0.55→0.49 · 1.00→0.93 · 7.58→8.28 M |
| Commons Lang total / JUnit total | 2.21 / 1.46 M | 2.18 / 0.96 M | 2.42 / 1.05 M | 2.35 / 1.07 M | 2.00→2.17 · 0.79→0.92 M |
| controlled: first / body / add class / insert early class | 1.25 M / 117 / 111 / 118 K | 1.32 M / 115 / 116 / 115 K | 1.52 M / 129 / 131 / 130 K | 1.59 M / 120 / 121 / 120 K | – |
| controlled: dependency +60 / wholesale / resource-only JAR | 41 K / 1.25 M / 148 K | 149 K / 1.32 M / 249 K | 164 K / 1.52 M / 264 K | 165 K / 1.58 M / – | – |
Normalization helps only Guava patch increments (−7…−17 % standalone, −6…−11 % in the JAR) and costs +12–19 % at first sight; greedy and hindsight both choose plain `outer` for Guava, Commons and JUnit. 80 Guava classes carry duplicate Class entries; the shared `ClassIndex` resolves them (**2 018/2 018 exact**). ZIP member identity alone (`struct-seg` 8.48 M) ≈ `outer` 8.73 M: exact DEFLATE adds nothing for JARs. Unlike DEX (3 DEX files of 9 MB, classes inseparable), a JAR exposes whole-class identity to the ZIP layer — the structural difference that makes DEX the target.
## 5. Economics, lock-in, overhead
| APK `outer+DEX 4K` (MiB) | DEX groups (payload+refs) | index nodes | DEFLATE recipe (units) | non-DEX plaintext | stored members + ZIP framing | recipe (inline, skeletons, refs) | total · new objects |
|---|--:|--:|--:|--:|--:|--:|--:|
| 2.0.1 first sight | 10.02 | 0.20 | 0.14 | 1.50 | 1.07 | 0.14 | 13.1 · 6 648 |
| 2.0.2 increment | 0.73 | 0.16 | 0.12 | 0.09 | 0.62 | 0.14 | 1.86 · 1 096 |
DEX alone, first sight (3 DEX, 19.6 MB): masked payload 5.50 M packed + references 4.34 M (string 0.81, type 0.89, proto 0.29, field 0.68, method 1.37, offsets 0.37) + nodes 0.16 M = 10.0 M vs CDC 8.51 M; payload-only ORACLE 5.6 M / 17.3 M history: **references are ~45 % of the cost** (hashes do not compress). Lock-in (APK): all CDC 46.3 M · all outer 45.6 M · all outer+DEX **27.5 M** · greedy per revision 45.6 M (never leaves the first sight's plan) · hindsight 27.5 M: first sight costs +1.6 M, later revisions save 19.8 M. Controlled 8-revision chain: CDC 4.04 M · normalized prod-CDC 2.89 M · grouped 0.87 M; JAR chain CDC 2.01 · outer 2.15 · +whole 2.45 · CP+body 2.46 M. Rolling 2-revision live set: CDC 19.4 M, outer 20.0 M, outer+DEX 16.7 M. Guava: greedy = hindsight = all `outer` 8.73 M (cp+body 9.42 M).
## 6. Reads, performance, memory
| request | production CDC | outer | outer + inner |
|---|--:|--:|--:|
| whole `classes.dex` 9.3 MB (APK member) | 3.69 M, 59 chunks | 4.14 M, 533 | 4.82 M, 2 743 |
| one code item (30–175 B) / 4 KiB inside it (bare DEX) | 56–61 K, 1 chunk | – | 5.5 K / 3.9 K data, **1.40 M (963 groups) with identity context** |
| one 4.8 KB class from a JAR | 68 K, 1 chunk | 1.4 K, 1 | whole 1.6 K · CP+body 1.7 K (2) |
DEX 8.9 MiB: parse 6 ms, walk 287 ms, encode + verifying decode 922 ms, decode 302 ms (29 MiB/s); peak RSS 275 MB (~30× the DEX, one DEX at a time). 12 MiB APK: analyse 5.4 s (exact DEFLATE parse 3.5 s), whole rebuild 3.15 s, RSS 386 MB. Guava (6.8 MB classes) encode+verify 394 ms, decode 209 ms. A ranged read needs the identity context (ID tables + strings ≈ 30 % of the packed file) — cacheable per DEX, not O(range).
## 7. Shared substrate, binref, portability, reuse
`INDEX_SUBSTRATE_PARTIAL`: `idxref` (300 lines) is real shared code — `PrefixIndex`, `ClassIndex` (duplicates), `Hash`, `PackComp/SplitComp`, `Sec` + the lab cutters, zigzag — used by both formats (8 % of 3.8 k lines); slot walkers, identity definitions, resolution order (DEX: offsets < string < type < proto < field/method; JVM: attribute names on the fly, 4 levels) and offset handling are per format. `binref`: conceptual only (address arithmetic vs units, no tables, no variable-length slots); separate package justified. No generic repz3 concept added (one kind number).
Portability: DEX parser / instruction decoder / identity resolver / recipe, JVM parser / CP resolver / bytecode decoder / recipe, `idxref` `CORE_PORTABLE`; DEFLATE adapter `PORT_SMALL_REFACTOR` (`ir` portable, lab closure glue); ZIP composition adapter `ANALYSIS_ONLY` (`archive/zip` segmentation, closure rebuild, no serialized framing). Reuse: unchanged `ir`, `repz3` contract/`Prove`, `lab` Acct/EconStore/CDC/pack rule, realworld pins; adapted: `lab` +81-line `Inner` hook, `IRRep`/`ChunkScaled` exports, `z3bin` delta → `pdelta`, `repz3` +1 constant; `torchz3` ZIP not reusable (stored-only). New: dexz3 2.2 k, jvmz3 1.3 k, idxref 0.3 k, driver 3.1 k, tests 1.8 k lines.
## 8. Final matrix, limitations, carry-forward
| target | decision | exact coverage | best real gain | performance | portability | debt |
|---|---|---|---|---|---|---|
| DEX | `PROMOTE_TO_I2` | 34 DEX / 137 594 classes / 831 940 methods, 0 fallback | history −47 % (38.4 → 20.3 M); patch 5.3 → 0.59 M | dec 29 MiB/s, ~30× RSS | `CORE_PORTABLE` | first sight +20 %; refs 45 %; ranged reads need 1.4 M context; no CompactDex/call-site tables |
| APK composition | `DEFER` | 5 real + 13 controlled APKs exact | −40 % (45.6 → 27.5 M) | 3.8 MiB/s rebuild | `ANALYSIS_ONLY` | needs exact-ZIP kind (I1), planner for lock-in (I4) |
| JVM class | `KILL` | 28 522 classes / 320 946 methods, 0 fallback | −7…−17 % Guava patch only | dec 32 MiB/s | `CORE_PORTABLE` | +12–19 % first sight, CP order is layout |
| JAR composition | `KILL` | 14 real JARs exact | none (outer ≈ struct-seg ≈ −29 % vs CDC is Z3-08's) | – | `ANALYSIS_ONLY` | – |
| shared substrate | `PARTIAL` | – | – | – | `CORE_PORTABLE` | walkers per format |
Limitations: one real APK family (F-Droid, R8, DEX 037; 039 only from D8); no second APK history, Kotlin/resources/native/signing semantics (signing block and `.SF` are opaque bytes, nothing verified); CP-permutation re-export not built (a reordered pool is layout state here). **Z3-33/I2 carry-forward:** keep index vs offset spaces separate (offsets by ordinal delta + shared-item classes); preserve variable-length slot widths with padded masks (.NET coded/heap indexes change width with table size, WASM LEB indexes); identity from native content with K0 = 3 + implied disambiguation (K0 = 2 churns); group at 2–4 KiB with index nodes; cheaper naming for repeated references is the open cost (refs ≈ half); first sight needs a planner.
**MANAGED_BINARY_WAVE_READY**
