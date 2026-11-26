# ZeroS3 C14 · Database and sparse volume revision histories

**C14_SPECIALIZED.** Opt-in sparse discovery gives the native archive
optimizer a useful candidate family beyond compressed artifacts. SQLite
qualified the existing generic authority path; its new semantic matcher
remains a research option for stable pages.

## Physical allocation and signed reads

Each paired trial started with the same raw packed seed. Every lane ran
GC, repack, compact and deep verify. The table uses finalized allocated
bytes across three revisions, including records and namespace.

| History | CDC | Generic archive | Sparse-file archive | Mixed with sparse |
| --- | ---: | ---: | ---: | ---: |
| ext4 rebuild and 64 KiB partition shift | 385,024 | 385,024 | 315,392 | 385,024 |
| ext4 in-place file replacements and 128 KiB shift | 458,752 | 458,752 | 438,272 | 458,752 |
| SQLite checkpoint, schema change and VACUUM | 708,608 | 413,696 | 413,696 | — |

On the rebuild, restricted sparse search added about 0.53 CPU seconds
over generic analysis and peaked at 124–128 MiB. The signed 4 KiB p95
was 1.45–3.48 ms versus 1.40–1.42 ms. Whole-object p95 was 20.4–49.0 ms
versus 17.8–17.9 ms, with 12 requests per trial. On the in-place volume,
signed 4 KiB p95 was 1.50–1.86 ms versus 1.44 ms. Both mixed-profile
runs rejected the rewrites under their read-cost budget.

Use `authority optimize -profile archive -order oldest -sparse-files`
to price the options. Each candidate is a normal XD record. The matcher
skips long zero runs during discovery and reuses verified zero extents
from the same object it already cites. Different buckets do not share
the sparse hint index. Whole authority closure costs govern selection.

## Mechanisms and external controls

- On the shifted ext4 pair, bounded native matching produced a 39,535-byte
  program versus stock XD at 73,151 bytes and xdelta3 at 34,755 bytes.
  Stock scanned the full 16 MiB as a source. The sparse matcher indexed
  749,568 bytes, and its 4 KiB decoder p95 was 0.26 ms versus 16.44 ms.
  The independent page prototype used 78,233 bytes and read in 0.09 ms.
- The in-place ext4 control kept the native patch sizes close. Bounded
  thin matching used 100,159 bytes and 71 ms encoder CPU on its shifted
  pair, versus 99,990 bytes and 131 ms stock. Unbounded zero runs made
  sparse reads slower on that control. The selected variant caps zero
  COPY segments at 256 KiB.
- SQLite leaf-page XOR missed overflow and relocation. In Go, following
  overflow links reduced the fixed-page patch from 249,861 to 52,359
  bytes across VACUUM, with 902,513 bytes of relocated COPYs nominated
  by row identity. Stock rolling XD used 21,975 bytes and xdelta3
  13,519. Stable updates showed a better tradeoff: 444 versus 426 stock
  bytes, 10 versus 62 ms CPU in one pilot. The gate for format-specific
  integration remains unproven across diverse SQLite schemas and churn.
- The ten-family panel included public MobileNetV2 weights, exact dtype
  tensors, Parquet, SST, operational events, compressed ext4 and entropy.
  Unrelated MobileNet required 13.16 MB for a 14.26 MB target even with
  zstd patching. Independent entropy grew. Synthetic same-layout tensor
  edits and real sparse updates have different economics.
- Restic 0.20.0 independently backed up and restored every revision.
  Final repository allocation was 1,806,336 bytes for SQLite, 1,417,216
  for the rebuilt volume, and 1,609,728 for the in-place volume. This is
  encrypted filesystem snapshot storage with separate cache and repository
  costs. Its restore timing includes opening that repository.

## Exactness

Every native restore matched the source SHA-256. SQLite replay checked
`integrity_check`, rows and schema, including a separate byte-identical
WAL pair rebuilt by the prototype. ext4 restores passed `e2fsck -fn` and
the sampled files matched through `debugfs`. The C13 reader read the
new XD records, and authority revert plus GC restored each object again.
The tests cover corrupted programs, bounded decoding, unrelated/dense
controls, same-bucket candidate scope, reopen, publication and recovery
through the existing format. Focused Go and race suites passed. The
benchmark uses loopback, two CPUs and warmed OS pages. Physical power
loss and multi-TiB volume behavior remain unqualified.

[Reproduction](REPRODUCE.md) · [Fixture digests](fixtures.json) · [Raw rows](results/)
