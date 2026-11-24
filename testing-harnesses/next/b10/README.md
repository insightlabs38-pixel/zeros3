# B10: deep container composition and compressed structural units

Linux amd64, Go 1.27.0, 4 vCPU VM; base `48afd29` (B9). Criteria fixed first ([PREREGISTRATION.md](PREREGISTRATION.md));
inputs [corpus.sha256](corpus.sha256), [make_inputs.py](make_inputs.py); reproduction [run.sh](run.sh); raw rows in
[results/](results/) (`tables.py`). Allocated bytes after optimize + gc + repack + compact; all 117 optimizer lanes
deep-verify with plan = materialized model; 1,796 reads byte-exact.

**Verdict: `B10_DEEP_REP_SPECIALIZED`.** Archive storage falls −62 to −66% against the strongest existing baseline on
two real RubyGems histories and −38% on two RocksDB SST histories, with no regression on any baseline or control;
Parquet gains −19 to −20% (pre-registered bound 0.80: Snappy 0.801, LZ4 0.813, so the structural criterion is not fully
met), Maven and OCI layers ≤ 2%. Reads cost B8's canonical tier: `mixed` keeps deep streams only for SST.

## B10-0: B9 grouped records
Grouped, 12 tests failed (B9 counted 11): 11 edit `.body` files, one expects the older build to name its feature first.
They pin the legacy layout; malformed/damaged-record checks run on either layout; grouped analogues show a damaged live
frame or lost group fails gc closed. Stale views: a pre-compaction location resolves to the re-appended frame, a stale
cross-process view reads the superseded generation exactly, and another process's online maintenance defers unlinks
while a shared opener holds the lease. **Fixed:** a mixed view (old dependent, newer base) failed the depth check; the
reader now reloads the group index once and retries.

## Architecture (`zeros3.go` 16e7, kind 16, store feature `splice`: older builds refuse such stores)
An object whose bytes hold independently compressed units is a representation over one body, its **deep stream** D: the
object with every unit decoded in place. Segments consume D in order: COPY, REP (a DERIVED child: B8's canonical gzip/
zstd/ZIP representation whose body is the next bytes of D) and PAGE (a Parquet page codec replayed from D: Snappy
predictor ids or commands, LZ4_RAW predictors/sequences, zstd tables, Brotli qualities). Units: tar members (gzip, zstd,
ZIP/jar/wheel), Parquet v1/v2 pages, RocksDB data blocks. D is an ordinary body (CDC, delta, windowed delta), so versions
share decoded content however it was compressed; a `.tar.gz` composes gzip ⊃ tar ⊃ ZIP (B8 wrapper → T → splice → D).
Children prove against their unit's SHA-256, objects at planning and apply (root and D digests must equal the plan);
children are gzip/zstd/ZIP only, units ≥ 4 KiB, D ≤ 32× the object, pages ≤ 16 MiB, else COPY. D is the record's one
base; a child's body is a table-resolved slice, walked in a scoped reach set (unscoped, a standalone container's stream
was dropped when a splice met the same child first). Units other objects hold byte for byte stay COPY (**hybrid
raw/replay**; Maven: 107 of 192 jars). Ranges touch only overlapped segments; the opt-in B9 cache keeps replayed pages
whole and shares child windows. The optimizer's read model maps sparse reads onto the exact D spans and replays.

## Materialized stores (allocated MB; b9 = B8 canonical + grouped records; b9x = b9 + codec replay, kinds 9/10, hard codecs)
| family | structure | cdc | b9 | b9x | **b10** | b10/b9x | b10-nodelta | units (kept) | recipe MB |
|---|---|---:|---:|---:|---:|---:|---:|---|---:|
| gems-rubocop (5) | tar ⊃ gzip ⊃ tar | 3.29 | 3.29 | 3.29 | **1.13** | **0.343** | 3.29 | 10 children | 0.27 |
| gems-ar (4) | tar ⊃ gzip ⊃ tar | 2.20 | 2.20 | 2.20 | **0.84** | **0.382** | 2.20 | 4 | 0.15 |
| maven (4+4) | gzip ⊃ tar ⊃ jar | 65.49 | 19.64 | 19.64 | **19.19** | 0.977 | 19.64 | 85 (107) | 1.11 |
| oci-python (3) | gzip ⊃ tar ⊃ wheel | 40.78 | 21.15 | 21.16 | 21.16 | 1.000 | 21.15 | 1 (2) | 0.07 |
| pq-snappy (4) | Parquet pages | 18.01 | 10.58 | 10.58 | **8.47** | 0.801 | 10.58 | 632 pages | 0.01 |
| pq-zstd (4) | Parquet pages | 16.42 | 11.93 | 11.93 | **9.51** | 0.797 | 12.54 | 632 | 1.06 |
| pq-lz4 (4) | Parquet pages | 18.09 | 10.43 | 10.43 | **8.47** | 0.813 | 10.43 | 632 | 0.01 |
| sst-snappy (4) | RocksDB blocks | 22.15 | 16.64 | 16.64 | **10.37** | **0.623** | 15.94 | 14,333 blocks | 0.06 |
| sst-lz4 (4) | RocksDB blocks | 21.11 | 19.14 | 16.67 | **10.39** | **0.623** | 15.97 | 14,194 | 0.06 |
| ts / ctl-rnd / ctl-unrelated | none / malformed / low overlap | 12.63 / 6.37 / 5.14 | 5.19 / 6.37 / 5.14 | same | same | 1.000 | same | 0 / 18 refused / 5 | ~0 |

B9 cannot reach the gems (its nested replay refuses gzip below depth 1); kind 16 builds each child at top level.
**Deltas between deep streams are the whole gain** (`b10-nodelta`: gems and Parquet at b9x; SST −4% from
recompression). Parquet zstd recipes cost 26% of the input (B8's negative result), yet the history still gains 20%;
`mixed` keeps deep streams only for SST (14.99/14.32 vs b9x 20.42/19.14 MB); `hot` stores none.

## Reads (cold medians, ms; [reads](results/reads.jsonl.gz))
| family | lane | 4 KiB | model | 64 KiB | 1 MiB | whole | concurrent 8× reads/s (p99 ms) |
|---|---|---:|---:|---:|---:|---:|---|
| gems-rubocop | cdc = b9x / **b10** | 0.42 / **39.9** | 0.43 / 35.6 | 0.62 / 74 | | 2.3 / 113 | 276k (0.5) / **138 (183)** |
| maven | b9x / **b10** | 21.9 / **25.4** | 17.5 / 18.2 | 32 / 35 | 83 / 147 | 362 / 384 | |
| pq-snappy | b9x / **b10** | 1.77 / **8.63** | 1.24 / 6.87 | 3.3 / 11.8 | 23 / 72 | 70 / 128 | 3,129 (27) / **1,481 (43)** |
| pq-zstd | b9x / **b10** | 6.17 / **19.6** | 0.99 / 15.6 | 24 / 25 | 180 / 144 | 723 / 346 | |
| sst-snappy | b9x (fixed) / **b10** | 4.12 / **4.02** | 0.82 / 2.97 | 6.3 / 12.7 | 40 / 112 | 303 / 420 | 928 (30) / **1,778 (47)** |

A gem read inside `data.tar.gz` pays B8's gzip restart over a delta-coded D (archive tier, as modeled); a Parquet read
replays its ~1 MB page; SST blocks are 4 KiB, so b10 matches b9x cold and serves twice its concurrent rate. The model is
within −10 to −30% for kind 16. **Fixed pre-existing defect:** kind-9 SST records had no range reader, so each 4 KiB
read rebuilt the whole table (282 ms, modeled 0.82); now only overlapped blocks (4.1 ms; kind-9 pricing still 5× low).
Opt-in 64 MiB cache, repeated / nearby 4 KiB: 2.52× / 1.74× (gems), 2.09× / 3.26× (Parquet), 1.20× / 0.95× (SST).

## Costs and negative findings
Optimize wall is 2-10× b9x (SST 10 → 99-131 s for 14k blocks per file; Maven 47 → 92 s), RSS up to 704 MiB, temp files
hold every D (60-480 MB written), peak allocation before reclamation 1.6-2.8× CDC. **Search regression found and fixed:**
with every nested unit decodable the OCI history planned 27.92 MB though the canonical plan holds 21.15 MB (B8's
incumbent excluded all canonical options at once). Maven and OCI gain ≤ 2%: unchanged jars and wheels already delta byte
for byte. Parquet stops near 20%: data revisions rewrite dictionary indices and RLE runs inside decoded pages.

## Correctness and recovery (`zeros3_b10_test.go`)
Exact whole and ranges at every segment boundary (gzip, ZIP, Parquet Snappy v1/v2, zstd, LZ4_RAW, Brotli; RocksDB LZ4,
LZ4HC, zstd); refusals (64 MiB bomb, multi-member and truncated gzip, damaged ZIP directory, fake gzip, truncated
Parquet, LZ4 without the hard-codec policy); 800 damaged tables never reproduce the object; a missing or damaged deep
stream fails reads closed and deep verify and gc refuse; shared children with order-independent reachability and
deletion; optimizer apply/reclaim/reopen/verify, plan = model, convergence, more options never plan worse; 8 concurrent
readers; **35 real SIGKILL cuts** (CAS flush, record written/renamed, frame written/synced, before publish; legacy and
grouped) each exact, reclaimed, verified, completed, converged; `-race` on the concurrent, stale-view and shared-child
tests; grouped: authority/B4/B6/B8/B9 and B10 suites. Root suite `go test .` at `50422f3`: pass (1,514 s).

## Limits and implications for the competitive benchmark
Deep streams are an archive tier for nested gzip (40 ms per 4 KiB) and a warm tier for SST; hot data stays CDC. Not
covered: xz/bzip2 members, containers nested in ZIP streams, Parquet GZIP pages, Zip64, nested splices, SquashFS; large
histories need persisted, incremental unit analysis. Against CAS/CDC/Xet systems expect byte wins on version histories
of compressed packages and LSM/columnar snapshots, losses in sparse-read latency, and little on unchanged-unit histories.
