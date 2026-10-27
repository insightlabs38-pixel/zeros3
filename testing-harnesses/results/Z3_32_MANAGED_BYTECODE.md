# Z3-32 — Managed bytecode indexed-reference frontier (CHECKPOINT 1 draft: characterization)
Laboratory only; production files untouched. Code: `z3/dexz3` (strict DEX reader + slot walker), `z3/jvmz3` (class reader + slot walker), `z3/pdelta` (Z3-12 pairwise delta, moved out of `z3bin` unchanged), `z3/cmd/z3mb` (corpus, attribution, baselines). Final report replaces this draft at checkpoint 2.
## Corpus
| class | artifact | provenance |
|---|---|---|
| REAL | F-Droid client 2.0-rc0, rc1, 2.0.0, 2.0.1, 2.0.2 APKs (12.5–13.3 MB; 3 × `classes*.dex`, DEX 037, 9.3+9.6+1.7 MB each; D8/R8 as built by F-Droid) | `realworld/sources.json` SHA-256 pinned, GPL-3.0-or-later |
| REAL | Guava 31.1 … 33.0.0 (5), Commons Lang 3.11 … 3.14.0 (4), JUnit 4.11 … 4.13.2 (5) JARs | `realworld/sources.json`, Maven `.sha1` verified, Apache-2.0 / EPL-1.0 |
| PRODUCER_WORKLOAD | 800-class generated Java library (10 methods each, ~1.9 MB DEX / 1.35 MB JAR), 11 one-mutation variants + a 7-step cumulative chain | OpenJDK 21.0.12 `javac --release 8`; R8/D8 8.7.18 (Google Maven, SHA-1 verified, sha256 58366f77…), `--release --min-api 24`; `jar --create --date=2021-01-01T00:00:00Z`; `z3mb workload` |
## DEX: where the changed bytes are (`z3mb attrib dex`; pairwise-delta literal bytes of the new DEX set against the whole old set, 24-byte anchors)
Slots (`dexz3.Walk`) are 32 % of a real DEX: ID-table indexes 11 %, instruction operands 8.5 %, class_data deltas 2 %, type lists 0.9 %, offsets 9.5 % (string_ids 3.3, code_off 2.4, debug_info_off 2.5, class_def 0.9). Code 46–48 %, string_data 23 %, debug_info 4 %.
| pair | raw literal | true literal (code/data) | index refs | offsets | debug/annotation | unchanged bytes dragged along |
|---|--:|--:|--:|--:|--:|--:|
| rc0→rc1 | 66.3 % | 1.2 % | 32.3 % | 14.2 % | 2.1 % | 50.1 % |
| rc1→2.0.0 | 66.3 % | 0.5 % | 32.5 % | 14.3 % | 2.1 % | 50.5 % |
| 2.0.0→2.0.1 | 28.5 % | 0.1 % | 36.0 % | 24.5 % | 2.9 % | 36.6 % |
| 2.0.1→2.0.2 | 61.8 % | 0.0 % | 33.4 % | 15.1 % | 1.8 % | 49.7 % |
(columns after the first are % of the raw literal.) Masking every index and offset slot takes the pairwise literal from 28–66 % of the new files to 0.4–2.1 %: nearly all "changed" DEX bytes are renumbering. Controlled: one changed method body → 14.6 % literal raw, **426 bytes** masked; add class 71.9 % → 677 B; insert an early class 74 % → 401 B.
Production CDC on the same bytes: 100 % new in 3 of 4 real pairs (70.6 % for 2.0.0→2.0.1) and 95–100 % for every controlled mutation; with all slots masked it is still 65–81 % new (chunks are 16–256 KiB, edits are scattered) — the Z3-12 result again: reference coverage is not the binding constraint, physical granularity is.
## DEX structure alone (packed MiB added to a store holding the earlier revisions, 96 B/ref, tree off)
| rep | rc0 | rc1 | 2.0.0 | 2.0.1 | 2.0.2 | total |
|---|--:|--:|--:|--:|--:|--:|
| production CDC | 8.43 | 8.41 | 8.47 | 5.32 | 7.87 | 38.5 |
| map sections (266 objs) | 8.37 | 8.40 | 8.45 | 8.03 | 8.45 | 41.7 |
| record-aware 16 K / 4 K units, raw | 8.79 / 9.87 | 8.60 / 9.04 | 8.87 / 9.41 | 4.80 / 5.02 | 7.70 / 8.03 | 38.8 / 41.4 |
| one object per code item (≈58 k/DEX) | 23.0 | 23.1 | 23.2 | 19.2 | 23.2 | 111.6 |
Structure without reference normalization does not beat CDC; per-method objects are ruinous at 96 B/ref.
## JVM: where the changed bytes are (`z3mb attrib jvm`, per class against the same-named old class)
| pair | raw literal | literal | CP/slot refs | debug/meta (LNT, LVT, StackMap, annotations, Signature) | other |
|---|--:|--:|--:|--:|--:|
| Guava 31.1→32.0.0 | 25.3 % of class bytes | 17.4 % | 31.2 % | 39.5 % | 12.0 % |
| Guava 32.1.0→32.1.3 | 1.9 % | 7.2 % | 31.7 % | 43.4 % | 17.8 % |
| Guava 32.1.3→33.0.0 | 4.8 % | 10.9 % | 31.8 % | 42.3 % | 15.1 % |
| Commons Lang 3.13.0→3.14.0 | 56.0 % | 31.5 % | 19.3 % | 27.3 % | 22.0 % |
| JUnit 4.12→4.13 | 32.7 % | 55.5 % | 15.8 % | 18.3 % | 10.4 % |
Whole-class identity: Guava 32.1.3→33.0.0 keeps 66 % of class bytes byte-identical (ZIP member identity already captures that); no changed class differs *only* by constant-pool renumbering (masked class bytes never become equal).
## Checkpoint-1 reading
DEX is the strong target: the hidden reuse is ≈ 98 % and ID/offset renumbering is its cause. JVM classes are small (3.3 KB), carry 15 % slot bytes and most of the churn is real (debug tables + literals), so the question is whether sub-class components pay. Next: exact representation, APK/JAR composition, economics.
