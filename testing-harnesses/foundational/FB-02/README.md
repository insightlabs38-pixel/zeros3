# FB-02: checksums, part bound, equivalence, restore index

Code on `research/next`: `3c73ef8` (M-A3), `20f13bc` (M-A4), `720f392` (M-A5),
`b926678` (M-A6), `6b82c4f` (gates). `1d4f36a` merges two commits another
session pushed meanwhile (competitive C17, C18). `main` unchanged at `2ac9cab`.
No on-disk format change; checksum persistence, replication defaults and
protocol v3 untouched. Go 1.27.0, standard library, one file. Evidence is in
`results/`; see `REPRODUCE.md`, and `API_COMPAT.md` for the wire changes.

## Verdict

| Milestone | Result | Status |
|---|---|---|
| M-A3 | five flexible checksums verified by header and trailer, both aws-chunked trailer modes, Complete FULL_OBJECT; bulk extension signed-only; peer reads bounded | PARTIAL: store/return is ADR-ID-03 (owner) |
| M-A4 | a part whose record exceeds one journal frame is EntityTooLarge before any write | COMPLETE |
| M-A5 | one identity rule, never the ETag; an idempotent commit writes nothing when equivalent | PARTIAL: default rerun is ADR-POL-01 (owner) |
| M-A6 | restore decodes a snapshot once per session; describe O(log n) | COMPLETE |

## M-A3 and M-A4

crc32, crc32c, crc64nvme, sha1 and sha256 are checked before publish, by
header or by trailer, signed and unsigned; a mismatch is `BadDigest` and
nothing is written. Server CPU per PUT without a checksum moved -1.5% to
+3.1% (median of 8 interleaved rounds against `5a10a9a`,
`results/ma3_ingest_cpu.txt`); crc64nvme runs at 1.97 GB/s, as fast as sha1
(`ma3_algo.txt`). A part record over 8 MiB (90,198 chunks, about 1.4 GiB of
content cut at the 16 KiB minimum) is refused with no state change; the
exact-cap record is accepted and replays (`ma4_parts.txt`).

## M-A5: equivalence before any write

Equivalent means the same object SHA-256, size, content type and user
metadata; an unknown identity is never equivalent. A commit with
`if_unchanged` (`replicate -if-unchanged`, and authority import) compares
before `publishManifest` and again inside the commit callback, so a
concurrent writer is never reported as unchanged. An idempotent rerun of 25
objects wrote no manifest and left the store byte-identical, at 2 requests
per object against 4 for an ordinary rerun. ETag-equal objects with other
content, metadata-only and content-type-only changes are replicated; fork
and restore refuse an ETag-equal destination with other content. Describe
returns `object_sha256` again. Two mutation controls (no pre-check; ETag as
identity) each fail the suite (`results/ma5_equivalence.txt`).

## M-A6: restore sessions

Synthetic snapshots of N entries naming one 4 KiB object (`ma6_profile.sh`):

| N | describe, fresh | describe, session | restore, fresh | restore, session | peak RSS, fresh / session |
|---|---|---|---|---|---|
| 1,000 | 1.1 ms | 25 us | 2.8-3.2 s | 1.2-1.8 s | 25 / 25 MB |
| 10,000 | 12 ms | 28-41 us | 124-129 s | 11.8-13.0 s | 45-46 / 37-39 MB |
| 100,000 | 112 ms | 31-34 us | not run (about 3 h) | 120 s | n/a / 171 MB |
| 950,000 | 1.09 s (HTTP) | 0.23 ms (HTTP) | not run | not run | 1.36 / 0.94 GB (20 describes) |

A session open costs one fresh describe. Restore costs 1.2-1.8 ms per object
at every size, 1.2 ms at 100k entries. The index retains 0.37-0.40x the
descriptor's bytes (91 MB at 950k against 250 MB). A snapshot deleted or corrupted halfway fails
the remaining objects with `NoSuchSnapshot` or `SnapshotCorrupt`, exactly as
without a session, 2-10x sooner. The transient peak of the one decode (2.6x
the payload at 950k) is the existing full read's, now paid once per restore
instead of once per object; lowering it needs paged snapshots (M-C3/M-F5).

## Gates

- G-FAULT (`results/ma_fault.txt`): a simulated crash at each of the 10 points
  an idempotent commit reaches and the 3 an at-cap part record reaches, an
  ENOSPC short write and an EIO fsync on the journal: every reopen holds the
  old or the new state and converges on a retry; GC keeps the live set. An
  equivalent commit reaches only the acknowledgement; an over-cap part,
  nothing.
- G-FUZZ (`results/fuzz_*.txt`, 60 s each, clean): aws-chunked trailers,
  checksum headers, the snapshot index against the fresh lookup, and the
  identity rule.
- G-SCALE (`results/ma6_scale.jsonl`): the 950k-entry snapshot (near the
  256 MiB descriptor cap) and the 100k-entry restore above. The queue's
  10-100M record stores were not generated here.
- Full suite at `6b82c4f`: passes in 1,386 s (`results/full_suite.txt`).

## Open

- ADR-ID-03 (owner): store and return checksums; per-part `Checksum*` in the
  Complete body is still ignored. No real-SDK interop run (installs need the
  owner's yes).
- ADR-POL-01 (owner): whether `-if-unchanged` becomes the default rerun. An
  old destination ignores `if_unchanged`; local `sync` is unchanged.
- Bit rot that keeps a descriptor's size and modification time is seen only
  after its session ends.
