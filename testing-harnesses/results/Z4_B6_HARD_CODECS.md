# Z4-B6 — Consumer-ranked hard-codec frontier

Verdict: **HARD_CODEC_FRONTIER_SPECIALIZED** (research only, RESEARCH_ONLY policy). Exact **LZ4** replay is a real advance for
three consumer classes (RocksDB SST, Parquet LZ4_RAW, SquashFS); Brotli/WOFF2, LZX/XPRESS and LZMA2 lose on measured oracles; LZMS is
data-blocked. Not claimed: production disk savings (CDC stays authoritative), Brotli exact replay (not built), LZ4HC level 10-12.
Base main `fc94bdc` (B5 merged; B2 #97 untouched, no draft imported). `zeros3.go` SHA-256 `ac29a564…f6b6f493` unchanged, root diff empty.
Code: `z3/lz4z3`, `z3/sqfsz3`, LZ4 kinds in `z3/sstz3` + `z3/parquetz3`, `z3/cmd/z4b6{,rank}`, `z3/b6cost`. Raw outputs and pins: `z4/b6/{README.md,results/}`.

## 1. Ranked candidate matrix (cumulative retained history, packed MB = production pack rule, 96 B/object + 36 B/ref where B1-priced)
| # | codec / consumer | corpus (class) | CDC prod | best CDC | decoded oracle (0-cost recipe) | exact replay, charged | verdict |
|---|---|---|---:|---:|---:|---:|---|
| 1 | LZ4 / RocksDB SST, 174 tables 631 MiB, 8-state chain (PRODUCER_WORKLOAD) | rocksdict 0.3.29 | 123.6 MiB | 123.6 | n/a (Snappy twin ukv 37.9) | **38.2 MiB** (ukv 8 KiB) | PROMOTE-quality, 0.31x CDC |
| 2 | LZ4_RAW / Parquet 8-rev chain 275 MiB (PRODUCER_WORKLOAD) | pyarrow 26.0.1 | 68.9 | 68.9 | 29.0 | **29.0** (logical 8 KiB) | 0.42x CDC; Snappy twin 90.9→29.0 |
| 3 | LZ4 / SquashFS typescript 5.3-5.7 (real releases, producer images) | squashfs-tools 4.6 | 34.8 | 34.8 | 19.7 | **23.7** (fine CDC 19.8) | 0.68x (0.55x fine) |
| 3 | same, three 0.160-0.164 | | 24.6 | 22.6 | 14.6 | **17.4** (fine 17.2) | 0.71x (0.76x fine) |
| 4 | LZ4HC levels 3-9 predictor (RocksDB `lz4hc`, CLI -3..-9, python-lz4) | rocksdict chain | 120.3 MiB | 120.3 | 38.2 | **38.2** | exact, same economics as #1 |
| 5 | Brotli / Parquet (pyarrow level 8) 8-rev chain | pyarrow brotli | 76.9 | 76.9 | 29.0 (ORACLE: Snappy-chain logical stream) | **not built** | SPECIALIZED/open: −62% oracle, re-encode reproduced 4/4 sizes |
| 6 | Brotli / WOFF2, 5 npm families, 39 releases | real histories | see §2 | | ≤ −19 % (fontawesome), others worse | not built | NOT_WORTH_IT |
| 7 | LZX / Windows 7 PE `BOOT.WIM`, 5 consecutive builds | archive.org, real | 540.7 | 505.6 (fine, raw) | 519.9 decoded (−3.8%) | not built | NOT_WORTH_IT |
| 8 | XPRESS / LZMS WIM/ESD | none reachable | – | – | – | – | BLOCKED (data access), not a negative |
| 9 | LZMA2 / SquashFS-xz, npm releases (new consumer vs Z3-39) | squashfs xz | 21.5 / 16.5 | 21.0 / 14.7 | 19.7 / 14.6 (−6% / −0.3%) | not built | NOT_WORTH_IT (Z3-39 negative preserved) |
Ranking by incremental benefit over the best existing representation, then by decoder cost. Producer-generated controls are labelled; nothing is a field-usage estimate.

## 2. Corpus provenance and decoded reuse
* **RocksDB** 9.8.4 via rocksdict, Z3-30 operation logs, `SST_CODEC=lz4|lz4hc|snappy` (one-line `gen_sst.py` change). LZ4 = 72.8 % of data-block bytes (191,940 blocks); the index block is LZ4 too, which is why all 11 LZ4 tables of Z3-38 fell back to CDC.
* **Parquet** Z3-31 pyarrow chain regenerated with `lz4|snappy|brotli`; **SquashFS** 4.6 over npm typescript×5 / three×5 (128 KiB blocks).
* **WOFF2** npm tarballs fontawesome-free×10, bootstrap-icons×8, katex×10, material-icons×5, @fontsource/inter×6 (1029 fonts); views: raw, Brotli-decoded stream, transformed tables, original sfnt tables (fontTools). 10 fontawesome streams are rejected by python-brotli yet decode fully with the CLI (unmodelled stream defect).
* **WIM** five real Win7 PE pre-release builds 7062/7070/7077/7100/7229 (x86, LZX, 130 MB each) and Win10 PE 10005/10034/10134/20136 (different arch/edition, not a history). Native lookup table: 88-90 % of each build's compressed resource bytes are new SHA-1 resources (resource-level reuse ≤12 %). Microsoft ESD delivery hosts are refused by the session proxy ⇒ no LZMS corpus.
| WOFF2 family (versions) | raw whole (prod CDC) | best decoded view | note |
|---|---:|---:|---|
| fontawesome-free (10) | 2.88 | tables 2.33 (fine), sfnt 1.59 | sfnt needs glyf/loca inverse + encoder; streams NOT reproduced by reference Brotli |
| bootstrap-icons (8) | 0.985 | 0.957 / sfnt 0.90 | −3 % / −8 % |
| @fontsource/inter (6) | 3.47 | 3.76 | worse |
| katex (10) | 0.269 | 0.29 | identical fonts across releases |
| material-icons (5) | 0.81 | 1.09 | worse (DEFLATE pack loses Brotli strength) |
Decoded storage loses to Brotli's own strength under ZeroS3's DEFLATE-only pack rule: the same effect that sank XZ in Z3-39.

## 3. Deep targets and exact architecture
Chosen by §1: **LZ4 fast family** (deterministic, tiny recipe) then **LZ4HC 3-9** and the **SquashFS** consumer. `lz4z3` (stdlib only, ≈1.5 kLOC):
`block.go` strict parser (offset/history/overflow/limit checks) and builder; an LZ4 block is a bijection of its sequence list, so recipes are exact by construction.
`recipe.go`: **explicit** sequences, **predicted** (profile id + acceleration/level), **hybrid** (predictor prefix + explicit tail); `Analyze` verifies every recipe against the stored bytes.
Profile registry (independently found, each verified against library output): `Default` lz4 ≤1.9.4 fast (u16 table + 4-byte hash < 64 KiB, u32 + 5-byte hash above), `Default5` (python-lz4 4.4.5 library: 5-byte hash even for u16), `Continue` (`LZ4_compress_fast_continue` fresh stream = RocksDB, always u32), `HC` hash-chain levels 3-9 incl. the repeated-pattern shortcut (levels >8).
`frame.go`: LZ4F frames (header verbatim, independent/linked blocks, block + content xxh32 derived or stored, skippable frames, opaque tail). `sqfsz3`: SquashFS v4 superblock/inode/fragment walk, data+fragment blocks decoded, everything else verbatim, decoded payload chunked by CDC.
Integration: RocksDB varint-size-prefixed LZ4/LZ4HC blocks (data and compressed index) in `sstz3`; Parquet `LZ4_RAW` pages in `parquetz3` (predictor ids inline, recipe member otherwise). No lz4 library, producer or Python is used by any decoder.
Strategies measured: re-encoding predictor (hit), explicit tokens (≈55-110 % of the compressed block → useless alone), hybrid prefix (−0..40 % on foreign fast encoders). Entropy-coding the sequences was not built.

## 4. Original compressed-byte replay proofs (independent oracles)
| producer / input | blocks | predictor hit | recipe |
|---|---:|---:|---|
| 48 python-lz4 fixtures (sizes 0-200 KB, accel 1/2/7/64) | 48 | 48/48 `Default5` | 3-4 B |
| lz4 CLI 1.9.4 frames: 14 of 20 streams (`-1 --fast=5 -9 -B4 -B5 -BX --no-frame-crc`, 1.8/6 MB) fully predicted | 14 streams | all blocks | 20-657 B/stream |
| python-lz4 hc3/hc9 (4 KiB + 64 KiB) | 138 | 100 % | 3 B/block |
| 480 pattern-heavy HC stress blocks (levels 3,6,8,9; `gen_hc_stress.py`, `B6_LZ4HC`) | 480 | 480/480 | 3 B |
| RocksDB LZ4 (data+index) | 191,940 | 100 % `Continue` | 1.34 MB for 631 MiB (7 B/block) |
| RocksDB LZ4HC chain | 70,230 | 100 % `HC` (levels 4-6) | 0.49 MB |
| pyarrow LZ4_RAW pages | 2,475 | 100 % `Default` | 0 (inline id) |
| mksquashfs lz4 (data+fragment) | 2,131 | 100 % | 8-42 KB/image (verbatim tables) |
| foreign: pierrec/lz4 v4.1.22 fast | 69 | 0 % (44 hybrid, 25 explicit) | 44 % of compressed |
| LZ4HC level 12 (`-12`, python hc12, squashfs `-Xhc`), pierrec HC5 | | 0 % | 73-75 % of compressed (optimal parser not ported) |
Negative: dependent (`-BD`) blocks, level ≥10, destSize (EROFS) are explicit/hybrid only; `-BD` frame replay costs 75-108 % of the stream.

## 5. Complete native-file exactness
Fresh-process proof (`verify_fresh.sh`: `z4b6 pack` writes recipe + components, a **separate** `z4b6 unpack` process rebuilds without the original, `sha256sum` and length compared externally): **174/174** RocksDB LZ4 tables (chain + 14 variants), **59/59** LZ4HC tables, **8/8** Parquet LZ4_RAW files, **20/20** SquashFS images (10 lz4, 10 level-12 lz4hc via explicit recipes). In-process 6-form round trips: 174 tables and 59 LZ4HC tables × 6 forms, `bad=0` on every line; `z3parquet rt` 8/8 exact, 0 fallback. Existing Snappy/Zstd/none behaviour unchanged (full affected-package tests pass).

## 6. Fully charged storage economics (standalone research representation; MiB unless noted)
| RocksDB LZ4 chain / row (store holds r0) | CDC prod | exact blocks | **ukv 8 KiB** |
|---|---:|---:|---:|
| first sight | 20.6 | 26.6 | 22.7 (+10 %) |
| 1 % update + compaction | 20.4 | 26.5 | **3.06** |
| insert / delete 2 % + compaction | 20.6 / 20.1 | 26.8 / 25.9 | 4.5 / 5.0 |
| full recompact, nothing changed | 20.4 | 26.5 | 0.78 |
| sorted re-export (same / half / double tables) | 20.5 / 20.5 / 20.5 | 26.6 | 0.92 / 1.01 / 0.94 |
| 8-state chain, cumulative retained | 123.6 | 159.5 | **38.2** |
Snappy twin on the same operation logs: 122.9 / 162.3 / 37.9 (Z3-30: 123.0 / 162.3 / 37.9, reproduced). LZ4HC chain 120.3 / 157.9 / 38.2. Wholesale/unrelated/random-value/re-key controls stay near CDC (−0.4..+11 %), no false reuse; first-sight lock-in of Z3-30 applies unchanged (greedy admission 85 vs 38).
Compared families, same charging: **Parquet** CDC 68.9 → dpages 76.0 (no dedup control) → logical 29.0; first-sight lock-in none. **SquashFS** 34.8 → 23.7 (prod) / 36.3 → 19.8 (fine); recipe+verbatim tables 0.02-0.14 MiB. **B1 generic similarity deltas** were *not re-run* on LZ4 inputs; B1's published RocksDB-SST ratio is 0.71× CDC generic vs 0.66× format (Snappy, other corpus), so B6's 0.31× is not matched by a codec-agnostic delta (inference, not measurement).
Authority: all rows are standalone research representations; CDC stays authoritative, an additive sidecar would cost CDC + representation (≈1.3×). No production saving is claimed. Hypothetical B2 authority would also need GC closure and the 7 B/block recipe indexes already charged above.

## 7. Performance, memory, ranges
| measurement | value |
|---|---|
| SST analyze (RocksDB LZ4, 631 MiB, streaming, fresh process) | 27 MiB/s, peak RSS 32 MiB, heap 17 MiB |
| SST decode from component files + SHA-256 each | 129 MiB/s, peak RSS 58 MiB |
| LZ4HC SST analyze | 6-10 MiB/s (HC re-encode per block) |
| lz4z3 block decode / predicted replay / explicit replay / HC9 replay | 62 / 64 / 192 / 4.7 MB/s (single core, 1 MiB block) |
| failed-predictor sweep (worst case, early abort) | ≈250 MB/s |
| SquashFS pack / unpack 10 MB image (whole-image in memory) | 1.0 s / 0.65 s, RSS 123 / 148 MiB (research grade, 12×) |
| range (RocksDB, largest table): 4 KiB in one block | CDC fetch 151 KiB (2 chunks) vs 2 components, 7.7 KB packed, 1.5 ms |
| point lookup (index + 1 block) | CDC 40.7 KiB vs 22.4 KiB packed (index read via 5 tail reads) |
| whole table 4.1 MiB | CDC 4.1 MiB vs 3.1 MiB packed, 63 ms |
Independent blocks (SST, Parquet page, SquashFS block, LZ4F independent block) make range decode O(block); linked LZ4F blocks need a ≤64 KiB history window.
Race: `go test -race ./lz4z3` (8 goroutines over pooled predictor tables) clean. Native `lz4 -d`/`brotli -d` were only oracles.

## 8. Hostile-input coverage (all fail closed)
lz4z3: every truncation, bad offsets/lengths/tokens, unterminated 255 extensions, 1 MiB amplification bound, size mismatch, recipe byte flips, wrong/short/long payloads, unknown predictor, predictor on linked block, absurd counts, frame descriptor/dictionary/checksum cases, short writer; fuzz `FuzzDecode`, `FuzzFrames`, `FuzzRecipe` (15 s each, clean). sqfsz3: truncations, superblock byte sweep, 6000 random mutations (accepted ⇒ exact), recipe flips, limits (image/blocks/decoded), short writer. sstz3: 1,500-point corruption sweep of an LZ4 table, recipe flips. Coverage-guided fuzzing of `FuzzEncode` stalls on 36 KB seeds, so the mutation test stands in; reported as such.

## 9. Negative and blocked candidates
* **WOFF2/Brotli**: oracle gain ≤19 % (one family) before any recipe; 10/15 real streams are reproduced by reference Brotli q11/lgwin22/font but a Go encoder port (Zopfli-style q11) is out of scope; fontawesome/bootstrap streams are not reproduced at all.
* **Brotli/Parquet**: strongest unbuilt lead (76.9 → 29.0, −62 %); Arrow level 8 is reproduced by google brotli q8/lgwin22 at all four tested sizes. Obstruction: no Brotli parser or encoder in the lab; recipe cost unmeasured. Prevalence of Brotli Parquet in the field is unknown.
* **LZX/XPRESS**: real Win7 PE history: decoded oracle −3.8 % vs prod CDC, worse than raw fine CDC; Z3-41's 16-17 % was same-content re-export. **LZMS**: BLOCKED, no reachable ESD.
* **LZMA2**: SquashFS-xz decoded oracle −6 %/−0.3 %: no implementation. **Zstd SquashFS** (existing `zstdz3`) oracle −9 %, below its 26 % recipe tax.
* **LZ4**: HC level 10-12, linked-block predictors, `destSize` (EROFS, never parsed), pierrec/Go and Java encoders are open; explicit recipes there cost 55-115 % of the stream.

## 10. Completion status
Breadth: all six families ranked against real consumers or classified BLOCKED (LZMS, real XPRESS). Depth: LZ4 fast (3 consumers, complete native files), LZ4HC 3-9, SquashFS complete. Brotli deep implementation was **not attempted**: ranking placed WOFF2 at NOT_WORTH_IT and Parquet-Brotli at an unmeasured recipe cost; recorded as the one open lead rather than as `B6_EXECUTION_INCOMPLETE`.

## 11. Production portability, B7/B8
`lz4z3` is CORE_PORTABLE (stdlib, no tables beyond two hash constants); the Sstz3/Parquet hooks are PORT_SMALL_REFACTOR; `sqfsz3` is a new parser (PORT_NEW_PARSER). Production integration needs B2-style representation authority, the admission planner (signals: LZ4 block seen, predictor hit-rate ≥ 95 %, weak codec ⇒ decoded wins) and a new kind number: all DRAFT_ONLY and **not** in this PR.
B7: Brotli parse + recipe cost or a q8 encoder port for Parquet; LZ4HC ≥10 and linked blocks; EROFS destSize; entropy-coded recipes. B8: admission and GC closure for the LZ4 kinds, using the 7 B/block recipe and the lock-in numbers above.

## 12. Branch, PR, validation
Branch `claude/zeros3-hard-codec-frontier-o81y6w` (runtime-designated; the same history is pushed to `z4/hard-codec-frontier`). Validation: `go vet ./...` (z3 module) clean; `go test` for lz4z3, sqfsz3, sstz3, parquetz3, snappyz3, zstdz3, repz3/..., lab, cmd/z3sst, cmd/z3parquet pass; evidence driver `run_evidence.sh` exit 0; fresh-process proofs above. PR and merge SHA are recorded in the final message and the PR itself.
