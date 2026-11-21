# C2A persistent similarity index contract

The index proposes bases for authority migration. The visibility journal, verified
manifests, snapshots and authority records remain the sources for visibility,
liveness, format support and byte identity. Existing candidate discovery remains
available when the index is disabled, stale, missing or corrupt.

## Identity and run layout

A catalog entry identifies a distinct logical object by its full SHA-256 and
verified logical length. Feature profiles have an explicit version that fixes
normalization, sampling and feature encoding. The initial profile reuses bounded
rolling windows and deterministic bottom features from the existing authority
and B1 work. Chunk-lane and format-specific feature IDs require new profile
versions. Identical bytes behind several keys share one catalog entry and one
posting per distinct feature.

Within a catalog generation, a nonzero uint32 ID maps to exactly one catalog
entry. IDs are assigned monotonically. A tombstoned ID can become live again only
for the same content identity. A full generation may assign new IDs while rebuilding. Files from different catalog
generations cannot be combined. The manifest binds the run and catalog versions
and a stable store ID from the format record.

The starting run is the research ZSR1 layout with a prefix directory, sorted
compact postings, CRC32C, and a manifest-level strong digest. Opening checks
arithmetic, lengths, offsets and ordering before any lookup. The reader locates
a feature's posting range through bounded reads and a bounded search rather
than allocating the whole prefix bucket. A hot feature gets an explicit
inspection budget across runs and a retained-candidate limit. Deletions and
older runs never require unbounded probing for a single feature.

## Snapshot of authoritative state

A publication fence binds the immutable generation to the validated durable
journal sequence, end offset and SHA-256 of the committed prefix. It also binds
the digest of the sorted, verified live snapshot roots. Incremental refresh
checks the previous prefix and derives the new fence in one sequential pass.
A changed committed prefix or snapshot set invalidates incremental reuse. A fresh opener compares the index
fence to the authoritative replay result. Unknown format or profile versions
and ahead-of-journal state make the generation unusable. An index behind replay
can catch up before use or fall back while a rebuild is pending.

C2A is offline and default-off. Use the existing exclusive store lock while
building or advancing a generation and while checking its fence. Stage runs
and catalog files in the index directory, sync each file, verify the bytes,
sync the directory, then atomically publish and sync the manifest defining
the active generation. Unpublished files are orphans. A compaction retains
all referenced inputs until the replacement manifest is durable and earlier
readers have released them. Startup recovery can ignore orphan files and
rebuild from the authoritative store. A failed refresh keeps the last complete
generation and exposes its actual fence.

## Object lifecycle

| Store transition | Index transition |
| --- | --- |
| PUT, completed multipart, copy, restore | After durable publication, count the new verified manifest root and index newly eligible content once. |
| Multipart part, retry, abort | Staging and parts never become candidates. An object enters only through the completed object record. |
| Overwrite, delete | Apply both new visibility and the journal's archived-version payload. Retained history can keep the old content eligible. |
| History prune and bucket removal | Remove the affected root references. Tombstone content only when its last eligible root is gone. |
| Snapshot creation or deletion | Reconcile its verified live roots and update the snapshot fence. Until reconciliation exists, mark the generation stale when the snapshot fence changes. |
| Migration, pin, unpin, and revert-all | Logical content behind an eligible root retains the same ID. Closure-only components and pins are excluded from the candidate universe. |
| GC, repack, tier movement | Catalog entries are independent of pack locators. Maintenance uses existing authoritative reachability and leaves the index advisory. |

Incremental maintenance uses the durable journal batch and a derived root-to-content
catalog. A replacement and archived version are reconciled together. Replays
are idempotent at the journal watermark. Incremental catch-up reads only
changed verified manifests and performs no whole-object scan when the digest
and profile already exist. Small journal updates stream the prior catalog and
retain only the identities needed by the updated roots. Rebuild enumerates
current, historical and snapshot roots individually before de-duplicating by
content. It distinguishes damaged or unreadable sources from absent roots and
reports incomplete coverage.

## Query and qualification

Only consider bases that the current migration scope and eligibility checks
allow. Resolve catalog IDs to content SHA-256 and require a matching verified
manifest in the permitted bucket and prefix. Apply depth, format, size and
policy limits before proposing a base to the planner. Feature scores and
posting matches have no authority over verification, retention or deletion.
Admission continues through the existing exact candidate replay, physical
verification and SHA-256 proof.

Bound open runs, per-run I/O, feature count, scanned postings and returned
candidates. Coalesce duplicate IDs and order ties deterministically. When a
budget prevents complete lookup, signal it in metrics and use the existing
fallback. Measure the recall tradeoff on retained histories and skewed features
before choosing final defaults.

The C2A gate covers crash cuts before and after each sync/rename, stale and
wrong-store manifests, bad lengths and offsets, CRC and digest faults, catalog
ID mismatch, tombstone loss, content deduplication under churn, snapshots,
prune, old/new readers and reconstruction with the entire derived directory
removed. Candidate sets after recovery must agree with the documented budget
for the same fixed fixture. No production lookup is enabled before the port,
incremental maintenance, rebuild and recovery qualify together.

The research precedents are the B1 discovery report, the P5 posting/run-set
measurements and the B8 C2 acceptance section. Their uniform synthetic posting
results set a baseline for investigation. Production qualification uses skewed
inputs and measured lifecycle costs.

The draft `zeros3 authority index-run-verify -run FILE` command checks an
individual ZSR1 run, optionally against an expected SHA-256 or with one
bounded feature lookup. The command does not open a store or publish index
files.

The draft `zeros3 authority index-build -store DIR` writes a sealed offline
generation under the existing store lock. It verifies logical content digests,
deduplicates catalog rows, bounds each posting batch, and verifies the synced
runs and catalog before the generation directory is renamed into place. The
optional `-apply` publishes a CURRENT pointer after rechecking the journal,
snapshots, catalog and runs. To activate a generation from a prior build or
refresh, run `zeros3 authority index-publish -store DIR -generation NAME`
using the directory's basename. This uses the same validation and publication
under the exclusive store lock. A stale or damaged stage leaves CURRENT
unchanged. `index-status` rechecks the active generation and can query one feature.

A standalone reader smoke test verified a 100262172-byte synthetic ZSR1 run
with ten million postings in one skewed prefix bucket, including six million
postings for one feature. It returned the newest 64 IDs using 10196 KiB peak
RSS and 0.542 CPU seconds. That test covers the reader and its query bound.
The production churn and lifecycle scale gate is separate.

The publication controls exercise an interruption before CURRENT changes and
reject a generation after a new durable journal mutation. A fresh P8 binary
opened a 2 MiB three-version store after explicit publication and passed deep
verification and the independent whole-object and range checks.

The draft `index-refresh` reuses an intact catalog and immutable posting runs
after verifying the committed journal prefix. It scans the durable suffix for
new put and multipart roots. Overwrites and deletes preserve their archived
references, and identical logical bodies reuse their catalog entry. Pruning
reconciles current, historical and snapshot roots and writes a checksummed
list of retired IDs. Content that becomes eligible again reactivates its
original postings. Snapshot fence changes trigger an authoritative rebuild.
Eight small tail runs compact within one bounded posting batch without
rereading object bodies. Generations allow at most 256 runs. An incremental
refresh that would exceed the cap rebuilds and leaves the previous generation
intact if the new generation also exceeds the cap. A feature lookup considers
the newest 64 distinct IDs across runs and allows up to 64 further IDs to
replace tombstones. It returns at most 64 live IDs. The same global scan cap
applies after compaction.

The lifecycle tests also kill a fresh process before and after generation
rename and CURRENT publication. Reopen and retry preserve the previous
authoritative object bytes. Post-crash data checks cover the exact content
and the complete active generation.

A legacy P8 server opened a store with an active v2 index, accepted a signed
PUT of an 86,658-byte body and returned identical bytes through S3 GET. The
index rejected the stale fence after that write and caught up without changing
the catalog generation. Both binaries passed deep verification of the updated
store. The same replay checked refusal and rebuild of the v1 prototype.

The C2B `index-inspect` command reports the publication state and counts inactive
generation and staging directories. It checks the selected generation under the
offline store lock. Inactive directories include both earlier sealed generations
and unattached builds. The command leaves derived files and object authority intact.

The migration command can reserve filesystem headroom with
`-reserve-free-mib`. It checks the current free blocks before staging encoded
delta or model output and again before publishing new pins and records.
Earlier staged output and retained shared packs remain charged. Structural
representation checks include unflushed pack or segment data when pricing
each new component. The reserve is an estimate. Actual write errors still
propagate, and aborted staging remains collectible.

The repeated-query reader retains the verified bucket directories of at
most 32 runs. Older verified runs read one pair of directory offsets on
demand. This bounds retained directories to about 8 MiB at the generation
run cap. The migration report distinguishes verified index open time from
candidate lookup time. The lookup timer covers the reported planning pass.
For migration, the planner inspects at most 128 recent posting IDs per
feature and counts up to 64 eligible discoveries. Scope, depth and size
checks happen before counting an indexed match. Entries already scored by
the in-memory planner do not consume the persistent match budget.

For a verified current index, `index-inspect` reports active catalog, posting,
and tombstone payload bytes. Its rebuild estimate uses live object count, the
16-feature limit and worst-case run headers. These payload figures exclude
filesystem block rounding, manifests and retained generations.
