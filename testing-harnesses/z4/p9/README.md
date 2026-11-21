# Durability and namespace scale

## Offline namespace checkpoint

`zeros3 namespace-checkpoint -store DIR` takes the exclusive store lock,
replays the complete journal and writes a derived checkpoint. It fsyncs a
staging file, renames it into the journal directory, then fsyncs that directory.
The fixed name replaces the previous checkpoint atomically. Each checkpoint
covers buckets, visible objects, retained history and incomplete multipart
uploads, with no locator data. The store format and journal remain compatible
with binaries that ignore the checkpoint.

`zeros3 serve -namespace-checkpoint -store DIR` opts into loading this
derived state. The reader verifies its SHA-256 trailer, store ID, journal
prefix SHA-256, offset, sequence and last-frame CRC before replaying the
journal suffix. The full prefix hash catches earlier journal changes even
when the final covered frame is intact. An invalid checkpoint falls back to
the existing full replay. Verify, GC and repack continue to use full journal
validation.

The focused race suite covers torn and corrupt checkpoint files, a changed
early journal frame, foreign stores, tail PUT/DELETE/multipart records and
process cuts on both sides of the rename. The process cuts do not model
physical power loss.

```sh
go test -race -run '^Test(NamespaceCheckpoint|Journal)' .
zeros3 namespace-checkpoint -store "$FIXTURE"
ZEROS3_P3_STORE="$FIXTURE" go test -run '^$' -bench '^BenchmarkNamespaceCheckpointOpen$' -benchmem -benchtime=2x -count=3 .
```

Use the P0 fixture builder to write ordinary acknowledged S3 objects before
comparing the two open modes. Label cache conditions and report repeated
wall time, allocation and physical checkpoint size.

## Local filesystem screen

`python3 -I testing-harnesses/z4/p9/local_io.py "$SCRATCH" --load "$LOAD"`
runs matched, hash-checked buffered and direct I/O trials. Scratch must be a
new directory on the tested filesystem. Buffered reads compare cache-drop
advice with an immediate repeat. Direct I/O uses aligned buffers. Sync
percentiles include both the 4 KiB write and its fsync.

In the first mixed-load screen the workspace mount was overlay. The visible
block inventory included a virtio disk and pmem, with no visible NVMe.
Buffered sequential writes with a final fsync ranged from 52.0 to 716.2
MiB/s and direct writes from 53.6 to 90.5 MiB/s. The respective per-write
fsync p99 ranges were 4.22 to 34.23 ms and 5.17 to 38.29 ms while the real
S3 fixture ingestion and qualification were active. These figures describe
this workspace mount under that load. The idle and service-level I/O
matrix remains pending.

[Local filesystem raw trials](results/local-fs-mixed.json)

## Checkpoint format iteration and fixture resumption

A 100,000-object store used one independently generated 256-byte S3 body per
key. The first checkpoint layout took a median 2.73 seconds to open against
1.43 seconds for the journal in that round. The second uses length-bounded
binary rows for visible objects, including the original manifest digests.
Its median was 1.94 seconds against 3.65 seconds for the same journal in that
round. Runs shared a two-CPU quota with other tests. In the second round,
allocated memory fell from about 103.2 MB per full open to 77.5 MB, and
the checkpoint file occupied 15,083,937 apparent bytes. An older binary
opened the same store and deep-verified 100,000 objects and chunks.

`p0bench mkstream` creates unique objects through signed conditional PUTs
in bounded batches. The cursor binds the binary, fixture seed, body size and
store identity. A fresh process reads back a deterministic sample. In a
deliberately rewound cursor test, the retry used exact GETs for existing keys,
checked all 799 bodies and kept one visible version per key. The separate
million-object fixture is used for the larger open comparison.

## Online pack publication experiment

The isolated epoch rig writes immutable packs and verifies every leased read.
A repack pins its marked packs during copy. Publication reconciles the latest
roots and keeps source packs for commits missed by the initial mark. Old
reader leases keep their packs until release, and deleted keys stay deleted.
The test raced 192 writes and 768 independently checked reads against 32
publications per trial. Namespace and locator metadata are in memory in this
rig. Durable locator publication, inter-process leases, repeated power-cut
recovery and tail latency at production scale remain open.

[Checkpoint comparisons and epoch race results](results/100k-checkpoint-and-online-pack.json)

## Bounded offline repack

`zeros3 repack -store DIR -max-source-packs N` limits a pass to up to
`N` source packs. Dry-run lists the selected packs and reports how many
were deferred. Apply retains the existing verification and publication
order and prioritizes reclaimable bytes. The test runs one source pack
per pass, reopens the store between passes and checks every live digest.

The limit bounds rewrite scope. Each pass still takes the exclusive store
lock and validates the live set. A fully live, incompressible pack can
remain eligible when explicit recompression is requested.
