# Z4-P1C proposal: packed-first small-object storage (log-structured segments)

Status: **proposal only** (not started, not part of P1B). Evidence: `pubsim -layout segment`, P1B ablations, per-object syscall counts.

## 1. Why the current layout has reached its ceiling

After P1B (per-digest publication claims, shared directory fsyncs, group-committed journal, manifest/chunk overlap) a 4-64 KiB PUT is still: one staged chunk file + one staged manifest file, 2 file `fsync`, 2 `rename`, 2 directory `fsync`, ~1 `mkdir` (young shards), 1 journal write + share of one journal `fsync` = **5.7 fsync, 6.0 openat, 1.25 mkdir, 2 rename per object** (strace, 4 KiB c8), and 1.0-1.5k ops/s on this VM against a tmpfs result of 2.7k ops/s in Z4-P0 (another VM).

* 37% of server CPU is `openat(O_CREAT|O_EXCL)` (inode allocation in ext4); sharding the staging directory did not change it (`rejected_tmp_sharding.jsonl`), so it is per-file cost, not directory-lock contention.
* With **every** fsync removed the same file operations still cap at ~2.7k ops/s (`pubsim -nofsync`); the filesystem metadata work, not the flushes, is the floor.
* Ablation (loose path only): removing the global publication lock gave +10-50%, overlap +13%, group commit +7%, shard-dir cache +3%, directory-fsync sharing ~0%. Nothing left in the file-per-chunk design is both safe and worth more than a few percent.
* The remaining 5.7 fsyncs/object are inherent: a chunk file, a manifest file, their directory entries and the journal are separate durable objects.

Objects of at least 256 KiB no longer hit this ceiling because P1B writes their new chunks into one pack (5-7 file operations per object instead of 50-70). Objects below that size cannot use that path without producing one tiny pack per object.

## 2. Prototype evidence (upper bound)

`pubsim -layout segment`: the same 4 KiB chunk + 700 B manifest + 250 B journal frame appended as one record to a shared segment file, durability by one group-committed `fsync` (leader/follower, no timer):

| concurrency | per-file layout (P1B design) | segment prototype |
|---|---:|---:|
| 8 | 1.5-2.1 k ops/s | 14-19 k ops/s |
| 32 | 1.8-2.2 k ops/s | 41 k ops/s |

No index, GC, read path or HTTP is modelled, so this bounds, not predicts. With the server's measured CPU split (file creation + staging/rename/dir work ~60% of CPU at 4 KiB) a realistic end-to-end expectation is **3-5x for 4-64 KiB PUTs** (an estimate, not a measurement), several times fewer file operations, and far fewer inodes (Z4-P0 measured 328k inodes and 28% extra allocated space for a 64k-object store).

## 3. Design

**Segments are packs in progress.** A segment is an append-only file `segments/<id>.open` that, once it reaches the pack target (or on idle/shutdown), is sealed by appending the existing pack-v1 index and footer and renamed to `packs/<id>.pack`. A sealed segment is an ordinary pack-v1 file: readers, `verify -deep`, `compact`, `repack`, tiering, bundles and old binaries need nothing new. Only the *open* segment is a new transient format.

Record: `[magic u32][digest 32][len u32][payload][crc32c]` (pack-v1 record layout plus a per-record CRC so a torn tail is detectable without the footer).

**Unified commit group.** A leader batches the requests that queued during the previous fsync (the P1B `commitQueue` pattern): (1) append every request's chunk records (and, for manifests below ~64 KiB, the manifest bytes as a record) to the open segment with one `write`; (2) `fsync` the segment; (3) append the journal frames and `fsync` the journal; (4) publish locator entries and apply the namespace in journal order; (5) acknowledge. Two fsyncs per **group**, independent of the group size, and zero file creations, renames or directory operations in steady state (segment roll: 1 create + 1 rename + 2 directory fsyncs per 64 MiB).

**Durability model.** Commit point is still the journal fsync. Barrier order is unchanged in kind: chunk bytes durable (segment fsync) before a journal frame that depends on them is written; a locator entry becomes visible to `casStat`/`casRead` only after the segment fsync that covers it (the P1B claim registry is reused: a digest is claimed from append to fsync). A crash leaves at most a torn tail: recovery scans the open segment from the last checkpoint, keeps records whose CRC verifies, truncates the rest; records not referenced by any durable journal frame are garbage collected like unreferenced loose chunks. Rename-atomicity and directory durability are needed only for seal/roll, where the existing pack publication ordering (fsync file, rename, fsync directory, then locator) applies unchanged.

**Index.** In-memory digest -> (segment, offset, length), ~48 B per chunk (1 M chunks ~ 50 MB), rebuilt at open from sealed-pack footers (already done for packs) plus a scan of at most one open segment.

**GC/repack.** Unchanged for sealed segments (pack-level liveness, `repack` rewrites partly live packs). The open segment is never collected.

## 4. Compatibility and format

* New store-format level (e.g. 10) raised before the first open segment is published; binaries that do not know it refuse the store (same posture as levels 6-9). Sealed packs stay readable by every binary that reads pack-v1.
* No change to manifests, journal records, wire protocol, CDC or object semantics; manifests may additionally be inlined in segment records, which is an internal location change behind `readManifest`.

## 5. Risks and open questions

1. Open-segment recovery and the interaction with `gc`/`verify`/`doctor` (they must treat `*.open` as live roots until sealed).
2. Small-record read amplification: sub-chunk reads from a 4 KiB record are fine; many small packs from idle rolls need a roll policy (size or age) to avoid re-creating the small-pack problem.
3. Memory for the open segment's unsynced tail is bounded by the group size; groups must be capped (bytes and records) to bound latency and memory.
4. Replication/bundles read chunks through `casRead`; they need no change if the index is the single lookup path.
5. Slow-fsync devices benefit most (two flushes per group instead of ~6 per object); not measured on real block hardware.

## 6. Recommendation

Do P1C if small-object ingest matters: it is the only remaining lever that changes the per-object file-operation count, and the prototype shows an order-of-magnitude upper bound. It is independent of P2 (read path): P2 can start now.
