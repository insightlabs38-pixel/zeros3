# Z3-37 — CDC/representation storage convergence

Verdict: **NOT_WORTH_COMPLEXITY** for a persistent-format change on present evidence.
Research only; production remains additive, authoritative CDC retained. No wire
layout, number, store version, persistent proposal, or migration is introduced.
Spans materially reduce selected sidecars, but every measured total exceeds CDC.

## Evidence and accounting
Shared Q6 capture, mechanically moved with `mv`, preserves its exact SHA objects,
roots, proof hashes, object costs and read counters (analysis timing excluded).
37 stages: SQLite4; Parquet3; RocksDB8/Pebble8; F-Droid DEX3; CLR5; Model3; Git3.
Public pinned F-Droid/Newtonsoft/Pythia histories and hiredis reexports are REAL/
REAL_REEXPORT; SQLite/PyArrow/SST are PRODUCER_WORKLOAD. Same Q6 provenance.
SST uses the explicitly scaled native operation history, not full-scale Z3-30.
Parquet is three existing PyArrow layouts, not an independent Spark history.
Selected candidates: sqlite, parquet8192, UKV8192, indexed4096, model-delta,
gp-zlib. All serialized roots reconstruct exactly before observation; all their
reachable objects, including metadata/base closures, enter the accounting.
Each alternative retains every authoritative CDC chunk across all revisions.
Cost = unique lab-compressed physical bytes +64/object, plus descriptor/index
bytes; common authoritative-root headers excluded. This is a physical-cost model,
not a byte-exact persisted production-root size or a latency benchmark.

Final cumulative total / authoritative CDC (lower is better):

|Class|Current >256K|Internal64K|Internal16K/all|Spans|Packs|
|---|---:|---:|---:|---:|---:|
|DEX|1.4812|1.4812|1.4810|1.4812|1.4454|
|CLR|1.9826|1.9826|1.9826|1.9826|1.9353|
|Model|1.0950|1.0836|1.0836|1.0150|1.9975|
|SQLite|1.3674|1.3674|1.3674|1.0194|1.3581|
|RocksDB|1.3301|1.3301|1.3301|1.3209|1.3159|
|Pebble|1.5273|1.5273|1.5273|1.5063|1.5044|
|Parquet|1.3205|1.3205|1.3205|1.3035|1.3448|
|Git|1.6355|1.6355|1.6355|1.1017|1.5787|

## A — whole-component SHA equality
0% of selected reachable component bytes equal a **current input** CDC chunk SHA
in all eight classes. This includes metadata; it does not say the existing >256K
internal chunks never share. Such existing internal sharing is priced in baseline.
Boundary coincidence is insufficient as the general convergence mechanism.

## B — internal production CDC
Reuses the exact production chunker at thresholds256/64/16/0 KiB; one-chunk
outputs stay direct. Charge logical SHA32, size/count varints and each SHA32+
length varint, once per logical object. Lower thresholds mostly cannot subdivide
small groups; Model gains about1.14 percentage points. DEX/Git tiny changes can
lose to descriptors. Current root validation rejects Size<=256K descriptors:
same field encoding does **not** establish backward compatibility. Existing kind
piece lists can sometimes split components without root changes; no production
adapter prototype or claim of universal compatibility is made here.

## C — exact spans, transient research structures
Suffix-array lookup searches the current input only, not an optimistic historical
index. Raw component-byte coverage: Model98.42%, SQLite99.18%, Git93.59%,
Parquet8.58%, RocksDB1.08%, Pebble1.60%, CLR0.0012%, DEX0%.
Distinct span metadata costs: Model56,854B; SQLite21,870B; Git614,265B;
Parquet430B; RocksDB1,564B; Pebble1,176B; CLR140B; DEX0B.
Counts include SHA32 + logical size/count and each SHA32+offset/length varints;
no persistent layout is proposed. Every replacement restores the full component
and checks its SHA. Original serialized reconstruction plus equal replacement
components proves compositional exactness; no alternative production root reader.
Cold first-min(4KiB,size) requests per component require full backing-chunk reads:
Model51.72x, SQLite26.27x, Git88.48x, Parquet40.50x, SST~27.5–27.9x;
CLR's tiny matched strings14679.74x. Full logical verification adds work beyond
this range metric and currently materializes components. These are cold byte
amplifications, not measured application latency or decoder RSS.
Reachability must mark every backing SHA, not merely the logical SHA or original
object root. Tests retain span-marked chunks independently of the original root;
missing/corrupt chunks, bad mappings, negative/overflow bounds fail closed.
GC must understand descriptor edges and keep chunks after CDC-root deletion;
pack GC must preserve each live backing chunk or rewrite descriptors atomically.
A logical hash prevents a valid chunk plus an incorrect mapping passing verify.

## D — physical packing; E — retirement
64KiB soft-cap packs keep logical SHA identities, append large objects intact,
charge48B logical lookup/object plus physical hash/64B overhead; exact extraction
is verified. Group compression helps some small objects but loses badly on Model.
Cold subranges decompress the whole physical pack; retained-dead-byte scenarios
and byte-read amplification are archived. One live tiny member can pin most of a
pack. Repacking needs transactional index replacement and digest verification.
This can be physical layout with unchanged logical semantics; it still needs
storage-reader/index/GC/recovery compatibility work. No persistent index proposed.
CDC retirement is architectural only: require proven full SHA replay, old-reader
routing, version negotiation, bundle/sync/verify/GC migration and rollback before
any deletion. Present CDC GET policy and legacy stores rule out unattended use.

## Gates and next action
Span bounds/hash/mark tests, exact-duplicate/repeated-cost tests, race, full lab/
root tests and vet pass; fuzz588,155 cases/25s. Root test is cached, production
unchanged. Canonical recapture and resource evidence under `testdata/q7-convergence`.
Capture64.5s/853.7MiB peak (full analyzer/index/store, not decoder-only).
Continue Q8 breadth; revisit spans only with native-range benefit and independent
producer histories that justify compatibility/GC cost. Do not freeze a new format.
