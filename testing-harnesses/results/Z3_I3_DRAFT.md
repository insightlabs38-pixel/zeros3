# Z3-I3 draft: bounded SST and Parquet integration

Status: I3_DRAFT_COMPLETE for the bounded prototype below. DRAFT_ONLY; do not merge.
PR #81; based on main 4390079 (Q8). Q9 remains blocked draft PR #80.
New production kind numbers 9/10 are confined to this unmerged branch.

## Port and contract
Mechanically extracted SST/Parquet/Snappy/Thrift cores with prodcodecport;
structured/structured-codec modes reuse production's existing Zstd declarations.
Immediate CAS writes replace the lab Bundle's retained component byte arrays.
The recipe bodies and component bytes remain the lab contracts; only the existing
lab/production envelope magic and version differ. Nine frozen native fixtures
match complete production roots byte-for-byte and prove every accessed dependency.
No new closure, producer executable or external decoder dependency is introduced.
Production remains one stdlib-only zeros3.go; root go.mod/go.sum are unchanged.
Store/journal/bundle/default policy is unchanged; CDC remains authoritative.

## Bounded subset
Creation supports modern RocksDB/Pebble SST with UKV record groups and exact
raw/Snappy/Zstd blocks, preserving native framing, indexes, filters and tails.
Legacy/LevelDB creation stays excluded. Pebble format7 and LZ4 stay CDC.
Flat supported Parquet pages retain logical streams, dictionaries, compression
recipes and exact layout. Unsupported chunks remain opaque; all-opaque files CDC.
The scalar/nested fixture correctly keeps a mixed logical/opaque representation.
Targets: 4 KiB normally, 32 KiB for files >=256 MiB to bound group metadata.
Per-page/block scratch: 16 MiB; individual values/keys: 1 MiB; components: 32 MiB.
Recipe heads <=16 MiB, explicit members <=262144, SST records <=2097152.
Source and cumulative declared decoded work retain the existing 1 GiB budget.
New guards cover prefix-expanded SST keys, Parquet canonical dictionary expansion,
canonical replay bytes, framing allocations and embedded Zstd parse/replay limits.
Previous-column decoder dictionaries are dropped and rebuilt only when needed.
The hostile prefix-sharing block and 128 MiB Zstd window reject before expansion.
Failed optional analysis retains the ordinary successful PUT and its CDC fallback.

## Exact corpus proof
All 162 Q8 inputs pass production proof or an explicit bounded fallback:
- SST: 66 exact, 44 fallback (33 new-Pebble, 11 LZ4/unsupported layouts).
- Parquet: 39 exact, 13 fallback (12 all-opaque, one oversized page declaration).
Every accepted file gets SHA/length reconstruction, full serialized reachability,
no hidden written object, and exact 4 KiB start/middle/end range comparisons.
All unsupported cases are retained; no canonical case was deleted or skipped.
A test initially treated mixed nested Parquet as wholly unsupported; it now checks
that the nested segments remain opaque and the entire file still reconstructs.
The absent-sidecar assertion now accepts the actual os.ErrNotExist contract.

## Memory and reads
The native PyArrow18.1 producer writes exactly 1,073,741,824 bytes: 267M INT32
values, Snappy, no dictionary, with 5,631,035 bytes of native footer metadata.
It is a synthetic memory control, not a natural-history storage win.
Full analyzer + CAS + serialized proof + reachability succeeds; source hashes,
wall time, RSS and heap samples are in testdata/i3-draft/memory.json.
Final control: 89.3 MiB peak RSS, 79.0 MiB heap growth, 133.1s concurrent-load wall.
Cohort peak RSS: SST175.1 MiB, Parquet98.2 MiB; sampled heap348/341 MiB.
Fresh-source 4 KiB ranges fetch 1,074,524 / 1,078,145 / 5,651,307 logical component
bytes at start/middle/footer: about 262x / 263x / 1380x amplification.
These counts exclude root loading and physical CAS compression/cache behavior.
Q8 corpus maxima are 11.11x SST and 400.07x Parquet for 4 KiB samples.
Private range buffers cap at 64 MiB. Public S3 ranges and whole GETs retain CDC.
Small-file Zstd model/index allocations remain a memory debt; the GiB control
must not be read as a uniform process bound across every codec/workload.
Cohort resources and sampled heap growth are preserved, including high samples.
Q8's lab storage economics are not relabeled as new production physical savings;
ClosureBytes in the probe is logical closure size, not physical disk accounting.

## Validation and durable evidence
Checkpoint1 ddd43d5: bounded cores and immutable lab identities.
Checkpoint2 563c1e1: native GiB proof and visible range amplification.
Final checkpoint adds prefix/Zstd hardening, closed corpus, gates and this report.
Root full suite321.583s, structured race41.069s, root/lab vet, full lab suite
and recipe fuzz pass.
Recipe fuzz: 55,812 executions /26.129s; exact logs/hashes in validation.json.gz.
Final-binary black-box checks: compat54, bundles24, kinds/lifecycle32, SIGKILL24,
stable-I1 rollback23. Old I1 reads unknown9/10 via CDC without rewriting stores;
unsafe deep verification/gc/repack refuse, lossless compaction preserves the graph.
Pre-I0 transcripts, old stores, full/delta bundles and replication remain exact.
Reproduce: q10_gib.py; q10_probe.py; q10_summarize.py; cmd/i3-fixtures.
Provenance pins source hashes, immutable vectors and existing Q8 producer inputs.

Leave this PR draft. Continue Q11 from latest origin/main; do not merge kinds9/10.
