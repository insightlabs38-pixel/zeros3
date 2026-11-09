# Z4-P1C format specification: segments, inline manifests, store level 11

Normative for `zeros3.go` section "4d. Small-object segments". Durability protocol: [`DURABILITY_MODEL.md`](DURABILITY_MODEL.md).
All integers little-endian, as in pack-v1 (`zeros3.go` section "4. Packs").

## 1. Decision: no new record format

The candidates (see the report, section 2) were: (A) shared sealed pack-v1 batches, (B) segments with a new CRC32C record, (C) segments + inline manifests. The chosen format is **C with byte-identical pack-v1 records and no new record layout**:

* a segment is a pack-v1 file *prefix* (header + records, no index/footer);
* sealing appends the ordinary pack-v1 index and footer and renames the file, so a sealed segment is a valid pack-v1 file that every existing reader accepts (`verifyPackFile` passes on it, byte for byte);
* torn-tail detection needs no extra checksum: a record is valid iff its payload hashes to the digest in its own header. The proposal's CRC32C was dropped because it is weaker than, and redundant with, the SHA-256 identity every chunk must satisfy anyway (a CRC would only be a faster pre-filter and one more field to specify, test and migrate).

## 2. Files

| path | content | lifetime |
|---|---|---|
| `segments/<seq:016x>.seg` | open segment (below) | until sealed, then renamed |
| `packs/<body_sha256:64hex>.pack` | sealed segment = ordinary pack-v1 | as any pack |
| `tmp/seg-torn-<seq:016x>-<offset>` | bytes cut from a segment's invalid tail at recovery | collectable by `gc`, never read |

`seq` is a store-local sequence (> 0, assigned as max existing + 1 at open); it only names the file and is not part of any record.

## 3. Open segment

```
offset 0   header   16 B   "ZSPK" | version u16 = 1 | flags u16 = 0 | reserved [8] = 0     (= pack-v1 header)
offset 16  record*          sha256 [32] | logical_len u32 | stored_len u32 | codec u8 | reserved [3] | payload   (= pack-v1 record)
```

Constraints on a *segment* record (stricter than pack-v1, which also allows deflate):

* `codec == 0` (raw), therefore `stored_len == logical_len`;
* `logical_len <= 262144` (`maxPackedChunkBytes`); `0` is allowed only for the empty chunk and then the digest is SHA-256("");
* reserved bytes are zero;
* `sha256 == SHA-256(payload)`;
* a digest occurs at most once per segment (a pack index cannot repeat a digest; the writer re-checks the locator before every batch);
* records tile the file: the first record starts at 16, each next record starts where the previous payload ended; there is no padding and no alignment.

A record is either a **chunk** or a **manifest**; the file does not say which. A manifest record's digest is `SHA-256(exact manifest JSON bytes)`, i.e. the `manifest_sha256` the journal already records for the root; its length is at most 65536 bytes (larger manifests stay files under `manifests/`).

Bounds and overflow: all arithmetic on offsets and lengths is done in `int64`/`uint64` after checking `stored_len <= logical limit` and `payload_end <= file_size` *before* the payload is read; a record that does not fit ends the valid prefix. Nothing read from a segment is trusted: a read re-checks the record header against the locator entry and re-hashes the payload.

A segment is appended to by exactly one writer (the batch leader) with `WriteAt(buf, durable_size)`; the durable size is the end of the last batch whose `fsync` succeeded.

## 4. Sealed segment (pack-v1)

```
open segment bytes [0, S)
index   52 B x N, in record order:  sha256 | payload_offset u64 | stored_len u32 | logical_len u32 | codec u8 | reserved [3]
footer  64 B: "ZSPF" | version u16 | flags u16 | record_count u64 | index_offset u64 (= S) | body_sha256 [32] | index_crc32c u32 | footer_crc32c u32
```

`body_sha256 = SHA-256(bytes [0, S+52N))`; the file name is its hex. The writer keeps a running SHA-256 over the durable bytes, so sealing never re-reads the data to name the file; it then proves the result with `verifyPackExact` (header, index, footer re-derived and compared; every payload re-read and re-hashed) before the rename. The CRC32C values are the ones pack-v1 already defines (Castagnoli); they protect the index and footer, not records.

## 5. Recovery of one segment (normative scan)

1. If the file is a complete, fully verified pack-v1 (`verifyPackFile`): finish the seal (rename into `packs/`, or delete if that pack exists).
2. Else require the 16-byte header; otherwise (or with no valid record) the file was never used by an acknowledged operation: delete.
3. Else take the longest prefix of valid records (section 3 constraints). Cut the rest (after copying it to `tmp/seg-torn-*`), `fsync`, adopt or seal.
Scan work is `O(file size)`; a segment is at most `2 x` the target (hard cap 64 MiB by default).

## 6. Store level and compatibility

| | |
|---|---|
| level | **11** (`storeFormatVersionSegments`). Level 10 is reserved by the unmerged draft #97 (representation authority); a build that supports level 11 refuses a level-10 store exactly as before (`supportedStoreFormat` accepts 1..9 and 11) |
| first operation that raises it | the first segment creation (first small-object PUT or small manifest commit under the default policy). Order: initialise warm/cold tier roots if the store is below level 5 (a level above 5 implies them; harmless to older builds) -> `FORMAT.json` rewritten durably -> create `segments/` -> create the segment file |
| stores that never create a segment | stay at their level; `ZEROS3_SMALL_OBJECT_STORAGE=off` keeps new stores at level 1 until the first >= 256 KiB object (level 2) |
| old binaries on a level-11 store | refuse to open (`unsupported store format version 11`) -- an explicit compatibility cost, not backward readability |
| new binary on an old store | reads everything; raises to 11 lazily on first small write |
| policy off after segments exist | segments and inline manifests stay readable and are recovered, sealed and maintained as before; only new writes go loose |
| sealed segments | ordinary pack-v1: they survive a manual downgrade only as far as the level allows -- the data is readable by any build that reads pack-v1 *if* the manifests are also supplied (inline manifests are records in those packs, which an old build does not look up) |
| level never lowered | no code path lowers a level; an abandoned experiment leaves a valid level-11 store |
