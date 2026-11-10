# Z4-P3 maintenance model: reachability, verification memo, parallelism, shared-open rules

Specification of what the Z4-P3 maintenance engine may share, reuse and run concurrently, and what it must never trust. Code: section "12a Authoritative reachability" and "12b Maintenance scan primitives" of `zeros3.go` (`reachabilityScanOpts`, `analyzeManifest`, `digestTable`, `liveSet`, `parallelFor*`, `verifyWholeObjects`).

## 1. The scan, stage by stage
`computeReachability`, `Verify`, `stats`, `gc`, `repack` and `compact` all call the one scan (`doctor` and the tier tools use `Verify` / `computeReachability`).

| stage | what | concurrency | state it may change |
|---|---|---|---|
| journal check | `replayJournalFn(f, nil)`: every frame's magic, version, type, length, CRC and sequence is validated; nothing is retained | one pass of 1 MiB reads | none |
| 1 enumerate | current objects, active multipart parts, durable snapshots (read fresh off disk), retained history are listed in the order the serial scan visited them; the first root naming a manifest UUID fixes the digest used to find it | sequential | the step list |
| 2 analyse | each distinct manifest UUID is read **from storage** (`loadManifestBytes`: inline record via `casRead`, or `manifests/<uuid>.json`), hashed, decoded and structurally validated (versions, chunk digests and lengths, length sum) in blocks of 32,768 manifests by a bounded pool | `maintWorkers()` goroutines, each with its own pack file handle; blocks are visited in physical (pack, offset) order | none: a worker returns a verdict |
| 3 merge | verdicts are applied root by root **in enumeration order**: issues, counters, the per-root "journal digest == digest of the bytes read" check, `ReferencedManifests`, the live digests (into one `digestTable`) and the optional representation closure | sequential | the result |
| 4 chunk pass | every distinct live digest is checked once: present with the claimed length (locator or file), and for a deep scan read and hashed through `casRead` (first verifying copy, fallbacks as before); a deep scan visits digests in (pack, offset) order | `maintWorkers()` goroutines | none; failures are collected and reported in table order |

A verdict is dropped as soon as the last root naming its manifest has been merged, so memory holds one block (plus manifests several roots share) rather than the whole store. Deep verification keeps them (`keepManifests`).

## 2. Validity rules (what is never trusted)
1. **No serving cache is consulted.** `Verify` still purges the manifest/chunk caches around itself; the scan reads manifests and chunk bytes through `casRead*` / `os.ReadFile`, never through `mcache` / `ccache`.
2. A manifest is usable only if its bytes were read in this run, hash to the digest **this root's** journal record names, parse, declare supported versions and have digests/lengths that sum to `total_length`. A root whose manifest is unreadable, mismatched or invalid contributes **no** chunks and raises an issue; any issue makes `OK()` false and destructive GC / repack refuse (`errGCUnsafe`). Nothing is skipped silently.
3. The live set is exact. `digestTable` / `liveSet` hold every live digest; there is no Bloom filter or other probabilistic membership structure and nothing approximate is consulted before a deletion.
4. A verdict, a table, a handle or a `liveSet` is valid for **one maintenance operation** and is never persisted or shared with serving. Destructive callers (`gc`, `repack`, `compact`, tier moves) take the exclusive store lock, seal the open segment and scan afterwards, as before; they re-derive the working set of each doomed pack from the pack itself.
5. Representation roots are walked by the unchanged closure code (`repReachCtx`), sequentially, from the merge, with the same depth/fanout/cycle limits; a corrupt reachable root is a fault in `RepFaults`, an issue, and fails destructive maintenance closed.
6. Corruption that is present before the scan, appears while it runs (`TestP3_DeepVerifyFindsCorruptionInjectedMidScan`) or between the chunk pass and the whole-object pass (`TestP3_DeepVerifyFindsCorruptionBetweenPasses`) is reported by `verify -deep`.

## 3. What deep verification checks, and the memo it may use
`verify -deep` is unchanged in meaning: (a) every live chunk reads back from storage and hashes to its name (first verifying copy, as `casRead` always chose); (b) every pack's footer/index parses and every record header agrees with its index entry (all packs, live or dead); (c) every live manifest's chunks, streamed in order, hash to `object_sha256` and total `total_length`; (d) representations reconstruct exactly.

Memo rules (all within one run, none persisted):
* A digest's *chunk-pass verdict* (ok / missing / corrupt / wrong length) is computed once per distinct digest instead of once per reference; it names the digest, not a copy, exactly as before (the first verifying copy decides).
* **Single-chunk objects** skip the whole-object read: when a manifest has one chunk and `object_sha256` equals that chunk's digest, the chunk pass has just hashed the stored bytes to that digest, which is the object digest. This is the only hash/read the memo removes. A manifest with several chunks is read again, every chunk verified against its own digest and folded into the object hash, so a copy that rots after the chunk pass is still found.
* A chunk that failed the chunk pass is not read again (its manifests are skipped), as before.
* Rejected as unsound: skipping the per-chunk digest check in the whole-object pass because "the digest was verified": that pass could then read a *different*, corrupt copy than the one the chunk pass verified and report a false whole-object failure (or, with another order, hide a bad copy). A location-aware memo (verify the copy that passed) is possible and would remove about one of the three SHA-256 passes on multi-chunk data; it was not taken (see the report).
* Multiple physical copies: unchanged. The chunk pass accepts the first copy that verifies; `verifyPacks` checks record headers of every pack, so a damaged *index/header* in any copy is reported even if another copy serves reads. Payload bytes of a second copy are not hashed by `verify -deep` today and still are not.

## 4. Parallelism policy
* `maintWorkers()` = `min(GOMAXPROCS, 8)`, or `ZEROS3_MAINT_WORKERS` (1 = the serial scan). Measured on 4 vCPUs: 1 / 2 / 4 / 8 workers = 850 / 567 / 390 / 420 ms (reachability, 100k objects); 8 oversubscribes 4 CPUs and gains nothing.
* Bounded everywhere: workers claim blocks of consecutive indices from an atomic counter (no queue, no goroutine per item, no channel); the repack stager keeps at most two windows (64 records / 8 MiB each) in flight; deep verification holds at most one chunk per worker.
* Physical locality: manifests and chunks are visited in (pack, offset) order, and each worker keeps its last pack file open (`packHandles`) so a run of neighbouring records costs one `open`.
* Deterministic results: counters and sets are order-free; issues are emitted in enumeration order (roots) or table order (chunks) and in manifest-UUID order (whole-object findings); packs are reported in pack order. The serial scan reported chunk findings in map-iteration order, so this is a strict improvement.
* Foreground load: a read-only `verify`/`stats` against a live server runs the same pool. `ZEROS3_MAINT_WORKERS=1` restores one core; there is no throttling or priority mechanism and none is claimed.

## 5. Shared-open and online semantics
* Read-only tools (`stats`, `verify`, `doctor`, `versions list`, bundle export, `repair` as a client) open with `OpenStoreShared`: they index the durable prefix of every segment and **never truncate, seal, adopt or relocate**. Z4-P3 closes the last hole in that promise: a shared opener also used to cut the journal back to the end of its own replay; a live server appending in between lost acknowledged frames and the store then failed to reopen (`TestP3_SharedOpenerNeverTruncatesALiveJournal`, deterministic, failed on `da88b4d`). A shared opener's journal is now read-only (`errJournalReadOnly`) and never truncates.
* Dry-run maintenance (`gc`, `repack`, `compact -dry-run`) and mutating maintenance keep the exclusive `LOCK` and quiesce/seal the writer first. Online mutation is not attempted: a coherent namespace snapshot exists (`snapshotNamespace`, `snapshotHistory`, `snapshotUploads` are taken under `Store.mu`) but they are taken one after the other, and a destructive sweep would also need a barrier against concurrent publication; the exclusive-lock requirement stays.

## 6. Intentional behaviour changes (all covered by tests)
1. A shared opener never truncates the journal (above).
2. Torn-tail truncation by the owner happens after the whole journal has been replayed and applied (it used to precede the apply); a journal error still wins over an apply error.
3. Chunk digests written in upper-case hex in a manifest name the same chunk and are keyed by the canonical lower-case name (previously the upper-case string was the key, so GC could have judged a lower-case loose chunk file unreachable).
4. `scanCanonicalChunks` (the P2 fast manifest scanner) refuses a manifest with any second `"chunks"` member, which `encoding/json` would have let override the first.
5. `stats` reads each current manifest once and uses one namespace snapshot for its per-object accounting and its reachability (it read every manifest twice, from two snapshots).
6. Chunk findings are ordered by table position, not by map order.
