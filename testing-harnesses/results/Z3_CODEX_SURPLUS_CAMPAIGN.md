# ZeroS3 surplus campaign — final decision record

2026-10-28. Audited main: `d61342734e7a1839d216f30a66bc0e74c4887c88`.
This summary is a subsequent documentation-only change. Queue Q1–Q16 and useful
unlocked expansion work are closed; blocked prototypes are not claimed complete
or merge-ready. Stop reason: only gated, unsupported or disproportionate work
remains under this campaign's rules, not a checkpoint/reminder/usage boundary.

| Queue | PR | Recorded outcome / report in this directory |
|---|---|---|
| Q1 runtime | [#72](https://github.com/insightlabs38-pixel/zeros3/pull/72), merged | I1A_PARTIAL; [runtime](Z3_I1A_REP_RUNTIME.md) |
| Q2 codecs | [#73](https://github.com/insightlabs38-pixel/zeros3/pull/73), merged | I1B_COMPLETE; [reserved Gzip/Zstd](Z3_I1B_CODEC_KINDS.md) |
| Q3 Git/OCI | [#74](https://github.com/insightlabs38-pixel/zeros3/pull/74), merged | I1C_COMPLETE; [integration](Z3_I1C_GIT_OCI.md) |
| Q4 debug symbols | [#75](https://github.com/insightlabs38-pixel/zeros3/pull/75), merged | all DEFER; [report](Z3_34_DEBUG_SYMBOLS.md) |
| Q5 native binaries | [#76](https://github.com/insightlabs38-pixel/zeros3/pull/76), merged | ELF/PE DEFER; [report](Z3_35_NATIVE_BINARY_RESCUE.md) |
| Q6 planner | [#77](https://github.com/insightlabs38-pixel/zeros3/pull/77), merged | PLANNER_NEEDS_MORE_EVIDENCE; [report](Z3_36_ADMISSION_PLANNER.md) |
| Q7 convergence | [#78](https://github.com/insightlabs38-pixel/zeros3/pull/78), merged | NOT_WORTH_COMPLEXITY; [report](Z3_37_STORAGE_CONVERGENCE.md) |
| Q8 breadth | [#79](https://github.com/insightlabs38-pixel/zeros3/pull/79), merged | bounded SST/Parquet research; [report](Z3_38_STRUCTURED_BREADTH.md) |
| Q9 Model/managed | [#80](https://github.com/insightlabs38-pixel/zeros3/pull/80), **open draft** | I2_DRAFT_BLOCKED; report lives on draft branch |
| Q10 SST/Parquet | [#81](https://github.com/insightlabs38-pixel/zeros3/pull/81), **open draft** | I3_DRAFT_COMPLETE bounded prototype, unmerged; report on draft branch |
| Q11 XZ/packages | [#82](https://github.com/insightlabs38-pixel/zeros3/pull/82), merged | NO_XZ_TARGET on tested histories; [report](Z3_39_XZ_PACKAGES.md) |
| Q12 scientific | [#83](https://github.com/insightlabs38-pixel/zeros3/pull/83), merged | HDF5/ORC/Arrow DEFER; [report](Z3_40_SCIENTIFIC_COLUMNAR.md) |
| Q13 WIM/ESD | [#84](https://github.com/insightlabs38-pixel/zeros3/pull/84), merged | DEFER, producer-only; [report](Z3_41_WIM_ESD.md) |
| Q14 send streams | [#85](https://github.com/insightlabs38-pixel/zeros3/pull/85), merged | full-send DEFER, incremental KILL; [report](Z3_42_FS_SEND.md) |
| Q15 bitcode | [#86](https://github.com/insightlabs38-pixel/zeros3/pull/86), merged | generic grouping KILL, normalization DEFER; [report](Z3_43_LLVM_BITCODE.md) |
| Q16 broad tail | [#87](https://github.com/insightlabs38-pixel/zeros3/pull/87), merged | five-format shootout; ONNX DEFER; [report](Z3_44_BROAD_TAIL.md) |
| E5 CRAM | [#88](https://github.com/insightlabs38-pixel/zeros3/pull/88), merged | framing KILL, entropy replay DEFER; [report](Z3_E5_CRAM.md) |

Production integrations are Q1–Q3 only. `zeros3.go` remains the sole production
source (37,850 lines), stdlib-only; root go.mod/go.sum are unchanged by the
campaign. Main production kinds remain 1–7: reserved Git 4/Gzip 6/Zstd 7 gained
bounded implementations. No new number ≥8, store/wire version, default-on mode,
persistent planner/refcount state or generic BASE was merged. CDC is authoritative;
optional representation creation follows existing proof/publication rules.
Main production/mod/sum are byte-identical to the Q3 merge after all later work.

Q1 reduced median full-scan allocation 26.3%; 1 GiB prefer whole GET 2.7→1.1 s,
server peak 161→143 MiB. Deep verify 216→218 MiB is **not** an RSS improvement.
Descriptor materialization/global GC map migration remain explicitly deferred;
ZS3T v1 was preserved. Transfers gain best-effort roots only after CDC commit.
Q2/Q3 mechanically port exact substrates, bounded temp-backed analysis and nested
composition. Unsupported producers fall back; no verification bypass was added.

Research positives are conditional, not storage promotions: Q8 DuckDB REP −37.3%;
Q12 standalone structured gains 50–57%; Q14 full-send payload reuse −64.7%; Q16
ONNX planes −26.8%. Retaining source CDC makes their additive layouts larger.
Q6 beam regret 0.0066% uses future evidence; it does not establish causal admission.
Q13 uses genuine native WIM producer work, without a natural Windows OS history.
Q15 independently matched 1,853,891 numeric records; generic grouping costs +17.2%.
E5 native block grouping loses to fine CDC; a Java fixture's seven auxiliary-tag
conversions remain a semantic negative, never disguised as a successful round trip.
No new research family received an unconditional production promotion.

Draft #80 head `43dfe4192ef0b25d035fac0850af263a7f866beb`: streaming Model prototype,
DEX/CLR whole-file memory debt; ordinary managed creation stays disabled. Model's
1 GiB probe is bounded, but natural model/managed breadth and code-size remain debts.
Draft #81 head `0a37846c610fa4268548957da7b0fde3c0d9c249`: bounded SST/Parquet
prototype; native 1 GiB proof ~89.3 MiB RSS, with explicit range amplification.
The drafts add 8,482/7,687 net production lines and 262/255 function declarations.
Their new kinds 8–12, code-growth decisions and production promotion remain
unapproved and unmerged. A bounded prototype verdict is not release readiness.

Validation is recorded per immutable checkpoint/report, with exact source hashes,
serialized SHA256/length/byte proofs, full reachable costs and cold-source ranges.
Production gates include complete root tests/vet, relevant races, old levels 1–5,
pre-I0 format-6 refusal, bundles/replication/lifecycle and actual process-crash tests.
Research final module tests/vet pass. Existing canonical cases/wire goldens were
retained; timing-dependent interruption was replaced by a real fetch gate.
Q12's broad legacy race attempt timed out at 600 s: it remains a timeout, not PASS;
changed-surface races pass. Q16's timed fuzz count was only 27; the separate
fixed-count 2,000-mutation PASS is reported honestly. No redundant race claim.
Native producers/oracles stay outside candidate decoder dependencies. Cached pinned
assets and shell extraction/copy were reused; branches and genuine checkpoints stay.

Expansion audit: E2 WOFF2/Brotli, E3 ONNX and E4 WAL received Q16 evidence;
additional codec work lacks natural-history/retained-source economics. E5 is closed.
E1 has no measured dictionary/window/EROFS/SquashFS consumer unlock. E6 still lacks
the required real LevelDB rebuild/restore history. E7 has no ≥100 MiB/s residual
coder beating plane+DEFLATE. Killed PyTorch/JVM/Wasm/PXR lanes were not reopened.
These are explicit prerequisites, not claims of completed unsupported codecs.

Largest blockers: duplicate retained-source storage; causal admission evidence;
new-kind/code-size approval; managed streaming; natural model/managed/OS histories;
descriptor/deep-verification RSS and unsupported codec profiles.
Next recommended queue item: none currently unlocked. Reopen I2/I3 only with their
approval gates and missing streaming/corpus/economics evidence; do not merge either
draft on a reminder. Broader lifecycle/GC work needs its own scoped evidence.
An hourly wake must read this terminal record and avoid restarting closed batches.
