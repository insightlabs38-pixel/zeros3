# Z4 A0 production audit

Verdict: **PRODUCTION_TRANCHE_NEEDS_REWORK**. No production change in A0.
Main audited: b5d176293090ecd284030f09aa6a3da46cc17155.
Immutable #80: 43dfe4192ef0b25d035fac0850af263a7f866beb;
#81: 0a37846c610fa4268548957da7b0fde3c0d9c249. Both remain drafts.
LOC is visibility only, never a rejection criterion.

## Independent tranches and implementation review

Model-8 can be extracted independently of managed kinds: ReaderAt metadata,
256 KiB plane/XOR windows, at most eight bases, cumulative 1 GiB inspection
budget, bounded metadata/tensor counts, depth four. A1 compatibility and A2
fresh breadth/malformed-input/aggregate-work checks remain promotion gates.
SST-9 and Parquet-10 are independently extractable from #81; shared builder/CAS
and existing Zstd decoder stay shared. Bounded block/page scratch is 16 MiB;
component scratch 32 MiB, values one million, cumulative inspection 1 GiB.
Unsupported features fall back rather than approximate source bytes.
A3 must choose an acceptable explicit analysis policy for slow codec paths.
DEX-11/CLR-12 ordinary creation remains disabled. Whole-file masked clones
(#80 lines 38717/42707), payload/group buffers and identity tables require a
real streaming architecture, not merely moving allocations to another helper.

AST declaration audit finds 260 genuinely new qualified functions/methods in
#80 and 255 in #81; no identical newly added qualified declaration overlaps.
This is not proof against equivalent algorithm duplication. The ports retain
source attribution and format-specific parser boundaries; no production decode
external executable was found. Main remains one stdlib-only production source.
Recipe counts are checked against encoded lengths before allocation; Model
width×element multiplication uses int64 and a 16 MiB cap, output sums are
checked before addition. These findings do not replace tranche fuzz/abuse gates.
Repeated derived children can replay work even when their emitted size is zero;
existing depth/output limits alone do not establish an aggregate replay bound.
A2 must test this explicitly before promotion; no exploit claim is made here.

A concrete quadratic helper is confirmed: repIXClassIndex.Put line 44181 scans
all preceding occurrences in the duplicate identity class for each reference.
Preselected n=1024/4096/16384/65536, three trials, exact Get roundtrips:
median duplicate Put milliseconds 0.276/3.282/44.491/714.835;
unique controls 0.022/0.128/0.414/2.727. Preserve encoded occurrence order and
replace the scan with a precomputed rank in A4. Model does not need this helper.

## Isolated production-path measurements

Inputs/SHA were selected before results: three real Pythia-14M checkpoints,
a tiny GGUF fidelity control, cached native RocksDB none/Snappy/Zstd files,
DuckDB Snappy/Zstd files, existing exact 1 GiB Parquet, public F-Droid DEX and
Newtonsoft CLR. Three fresh-store cohorts each except one 1 GiB run: 34 cases.
Authoritative CDC ingest and namespace commit precede optional encoding.
All completed cases reload the serialized complete root and compare the full
cold reconstruction byte-for-byte, length and SHA; all 34 are exact.
Compiler/producers excluded. Times below are medians, seconds; RSS is maximum
sampled encode-phase increase over phase entry, not total process RSS.

| Input | Encode incl CAS | Full proof | Cold decode | Incremental RSS MiB |
|---|---:|---:|---:|---:|
| Pythia step1000, 26.84 MiB | .1409 | .1080 | .1067 | 3.19 |
| Pythia step2000 | .2390 | .1934 | .2012 | 8.54 |
| Pythia step3000 | .3691 | .2550 | .2728 | 17.98 |
| GGUF 1376 B fidelity only | .0003 | .0001 | .0001 | .85 |
| SST none, .93 MiB | .0070 | .0044 | .0043 | 3.09 |
| SST Snappy, .49 MiB | .0151 | .0064 | .0064 | 1.31 |
| SST Zstd, .42 MiB | .8116 | .1667 | .1231 | 28.73 |
| Parquet Snappy, 28.77 MiB | 2.4203 | 1.1134 | 1.0246 | 30.22 |
| Parquet Zstd, 21.85 MiB | 6.2700 | 1.5879 | 1.5114 | 56.37 |
| Native Parquet 1024 MiB | 16.8428 | 9.1057 | 8.5097 | 51.48 |
| DEX 8.87 MiB | .7449 | .3476 | .4169 | 213.69 |
| CLR .68 MiB | .2062 | .0813 | .0760 | 42.93 |

The GiB control achieves 60.8 MiB/s encoding and 120.3 MiB/s cold decode;
its total process maximum RSS is 71.32 MiB. It is a memory/latency control,
not a universal codec or storage-benefit result. Its optional physical growth
is 1,072,767,789 bytes on top of retained CDC; no convergence claim follows.
GiB sink Put time is 2.713 s, flush .095 s, publication .000253 s.
Encode-minus-sink includes parsing, transforms, hashing and metadata, not pure
parse time. Every phase and heap/RSS sample is retained in phase-summary.json.
10 ms sampling can miss short peaks; process high-water marks are separate.

## Old-binary behavior and compatibility decision input

Disposable committed/proven roots test kinds 8,9,10,11,12 with real I1 code.
I1 verify/deep rejects unknown kinds; doctor with explicit retirement removes
valid newer roots as faulty. CDC remains authoritative, but accelerators are
silently lost from the newer implementation's perspective. Model GC apply
refuses deletion; dry-run reports an incomplete live set. Stats succeeds.
LOCK owner metadata changes on maintenance opens and is recorded separately;
root deletion is a durable representation mutation, not merely a lock artifact.
Thus format-6 read fallback is insufficient for accelerator preservation.
A store capability floor is cleaner than pretending old retirement is harmless;
A1 must prove the full multi-generation operation/crash matrix. A single floor
must also protect later new kinds from earlier Z4 binaries that know only 8.

## Evidence integrity and next item

Scripts/private probes and compressed raw results are under ../z4/; production
sources are copied mechanically from immutable draft blobs and main renamed
only in private binaries. Source SHA and preselected corpus are retained.
Initial cold probe omitted prior-base descriptors: failed run preserved, then
fixed by serialized complete-root reload. The first v2 Model cohort lost outer
resource-recorder status; four exact rows were independently checked, process
exit/RSS is explicitly unknown. Two other fresh Model cohorts exited zero.
An invalid CLI flag in an auxiliary compatibility attempt is retained separately;
corrected invocations use -store and do not count usage errors as refusals.
Next: A1 compatibility design/matrix, then Model-only A2, bounded-policy A3,
and architectural managed A4. No production tranche is claimed READY yet.
