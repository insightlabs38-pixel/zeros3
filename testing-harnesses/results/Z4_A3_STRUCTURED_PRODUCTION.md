# Z4 A3 bounded SST/Parquet production

Verdict: **STRUCTURED9_10_PARTIAL**, a clean bounded subset; MERGE_ALLOWED.
PR #93, based on main 90eed88 (A2 #92). Original #81 remains a draft.
Production SHA256: 34262c8eda1025bfc6110582cdc6dd193a1455578ed7a3fd89693e552b3164ce.
Mechanically extracted #81 0a37846c appended cores onto Model-capable main;
existing Zstd is shared, not copied. One stdlib-only production source remains.
LOC is tracked, never a rejection criterion. Creation still defaults off.

## Supported subset and admission decision

SST kind 9: supported modern RocksDB/Pebble layouts, none/Snappy/Zstd, UKV groups,
exact native framing/index/filter/tail. LevelDB creation, Pebble table-format 7,
unsupported LZ4/layouts remain CDC. Parquet kind 10: supported flat PLAIN/RLE/
dictionary/BYTE_STREAM_SPLIT/DELTA_LENGTH_BYTE_ARRAY, none/Snappy/Zstd, verified
opaque regions. Mixed logical/opaque is retained; all-opaque files use CDC.

Overall source/declared-decoded inspection budget remains 1 GiB. Scratch block/
page ≤16 MiB, values/keys ≤1 MiB, components ≤32 MiB, recipe heads ≤16 MiB,
explicit members ≤262144, SST blocks/Parquet pages ≤65536, SST records ≤2097152.
The measured 1 GiB Snappy control is acceptable for explicit synchronous opt-in.
Expensive Zstd creation is additionally capped at **4 MiB decoded /512 frames
for SST**, **128 MiB /1024 pages for Parquet**. Excess returns to ordinary CDC.
Caps bound work rather than promise a fixed wall time. No daemon/backfill CLI is
introduced to conceal latency. Existing proven roots remain decodable above
creation quotas; two deliberately oversized pre-existing roots verify exactly.

An uncapped diagnostic on a native 32 MiB-valued SST (1.69 MiB compressed source)
spent 84.6 s encoding and sampled high transient heap/RSS under concurrent load.
It is retained as a negative diagnostic, not an isolated throughput comparison.
The new preflight rejects that workload before expensive optional analysis.
Native over-budget SST and 256 MiB Zstd Parquet controls keep successful streaming
PUTs, exact full/range reads, zero optional roots and no floor-eight publication.

## Correctness, retention and cumulative compatibility

Checked uint64 accumulation rejects a malformed Parquet recipe whose sixteen
2^60 stream-node counts wrapped to zero; its original failing test is retained.
Cumulative guards reject before retaining excessive SST command/block metadata,
Parquet page headers or layout payloads. Short layout prefixes detach from decoded
page backing arrays, including empty slices; they cannot pin whole pages.
All frozen encoder outputs stay byte-identical. No whole-file analysis copy is
introduced. An optional failure never changes the authoritative successful PUT.

Publication traverses all derived kinds and durably raises floor **8** before any
SST/Parquet root, while retaining Model floor 7 and earlier decoders. Merely opening
a store does not upgrade it. Retirement never lowers the floor. Old format-7 Model
binaries and older generations refuse local opens/maintenance before journal use.
Four older local generations produce 63 genuine floor-eight refusals plus one
pre-I0 unavailable CLI flag, explicitly not counted as a safe refusal. No durable
payload/namespace/root mutation occurs; LOCK owner metadata is recorded separately.

Real valid native revisions pass authenticated wire/maintenance matrices. The
final small-Parquet matrix has 79 PASS checks and snapshot deletion another 15.
Older clients sync/replicate through compatible servers; portable full/delta bundles
restore exact content without importing optional roots/floors. Compaction, repack,
tier moves, GC, doctor/retirement and history pruning preserve exact content.
Cumulative Model+SST+Parquet restart proof passes. Ten direct ordinary-PUT crash
cases preserve the prior namespace, GC safely and retry. Ten real SIGKILL cases
cover before/after floor, before/after rename and published roots for both kinds;
all restart/deep proofs pass, and the Model-only binary refuses every floor-8 state.

## Native exactness and isolated production-path costs

All original **162** native cases are retained, recovered by SHA before rerun:
SST 66 exact/44 fallback; Parquet 39 exact/13 fallback. Every accepted root ID is
identical to the immutable draft, including three exact private range samples per
file. Nine frozen fixture encoder/decoder roots also match unchanged.
Private 4 KiB amplification reaches 400.07× in this cohort; the older GiB probe
records 262×/263×/1380×. Public whole/range GETs may retain CDC; no private-range
speed advantage is claimed or required.

Ten preselected inputs, 28 fresh-store/process uninstrumented samples (three each,
one GiB run) plus ten separate profiled samples: all **38** reload complete
serialized roots and reconstruct byte-for-byte with length/SHA equality.
Ordinary CDC ingest/namespace commit precede optional encoding and are separately
timed. Compiler, native producers and downloads are excluded; OS cache is not flushed.
Times below are uninstrumented medians; RSS is maximum process high-water mark.

| Native input | Optional encode s | MiB/s | Full proof s | Cold decode s | RSS MiB |
|---|---:|---:|---:|---:|---:|
| RocksDB none, .93 MiB | .0065 | 142.51 | .0047 | .0045 | 14.36 |
| RocksDB Snappy, .49 MiB | .0171 | 28.77 | .0078 | .0071 | 14.00 |
| RocksDB Zstd, .42 MiB | .7619 | .55 | .1397 | .1291 | 51.68 |
| DuckDB Snappy, 28.77 MiB | 2.3522 | 12.23 | 1.0883 | 1.0394 | 41.99 |
| DuckDB Zstd, 21.85 MiB | 6.3717 | 3.43 | 1.6041 | 1.6122 | 68.79 |
| PyArrow 32 MiB control | .7686 | 41.64 | .5645 | .4558 | 42.80 |
| PyArrow 256 MiB control | 4.5486 | 56.28 | 2.9888 | 2.4796 | 59.33 |
| Cached PyArrow 1024 MiB | 16.3816 | 62.51 | 9.9467 | 9.3681 | 84.91 |
| RocksDB ~32.8 MiB control | .3675 | 89.13 | .1641 | .1624 | 25.74 |
| RocksDB ~262.1 MiB control | 1.7632 | 148.63 | .6078 | .6591 | 54.67 |

Scale controls use native writers and controlled values, not real dedup economics.
The cached GiB file is reused, not regenerated. Private AST timing defers measure
exclusive stages without changing production source. GiB example: metadata/footer
opens .0041 s, semantic planning/codec 3.9982 s, component/revalidation pass 9.7517 s,
CAS 2.5979 s, other/preflight .0288 s. Page parsing/revalidation inside passes stays
attributed to those passes; these are operational stages, not pure parser timings.
Proof/publication are separate; median GiB publication is .0142 s. All phases,
heap/10 ms sampled RSS, source hashes and profiler declarations are retained.

Strong negative: the GiB optional representation adds 1,072,767,789 physical file
bytes above retained CDC. This is not storage convergence; B2 remains DRAFT_ONLY.

## Validation integrity and next

Final root suite PASS (196.031 s), structured race PASS, root/harness vet PASS.
Recipe fuzzing passes, including 5712 executions with bounded minimization time;
all seeds remain. A rare completed-body/server-counter race was retained, repaired
by joining handlers before the unchanged assertions, and passed 100 repetitions.
A tiny-file harness generated a malformed negative-start range. Its input/request
remain: valid starts are clamped, and malformed ranges check the existing frozen
full-object response contract. Failed oracle runs are preserved; none was skipped.
Complete evidence, producer provenance and phase rows are under ../z4/testdata/
a3-structured. Checkpoints cf00639, 5f74649, 44b616f plus final evidence closure.
Next A4: genuinely bounded managed analysis and compact/reference-state work.
