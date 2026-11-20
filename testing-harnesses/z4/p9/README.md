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
