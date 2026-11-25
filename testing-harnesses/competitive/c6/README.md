# ZeroS3 C6 · Physical layout and recompression

**C6_SPECIALIZED.** An opt-in exact proof retains identical replacement
packs and avoids the staging writes of a repeated full-live repack.
The largest benefit was on opaque or already finalized histories.

## Verified repack

`repack -apply -recompress -keep-unchanged` considers the existing planned
batch, including tier, ordering and logical sizes. It verifies the existing
pack framing, checksum and records, then compares each raw or DEFLATE payload
with the current encoder output. A match keeps the original pack and its
locator. Different batches use the established replacement path. The code
reuses the existing pack format, feature gates and publication transitions.

The raw-only pilot missed the MobileNetV2 pack because fourteen records
compressed. Rechecking exact encoded bytes made the mixed pack eligible.
The bounded version retains only each candidate's leading digest and pack
reference during planning.

| Finalized store | Allocated bytes, both lanes | C2 pack bytes staged | C6 pack bytes staged |
| --- | ---: | ---: | ---: |
| MobileNetV2 checkpoints | 28,487,680 | 28,406,850 | 0 |
| Independent entropy | 12,689,408 | 12,610,799 | 0 |

In the bounded run, MobileNetV2 repack consumed 0.207 → 0.099 CPU seconds.
The process reported 55,504 → 8 output blocks, including non-pack output.
Both arms kept the same pack ID and restored both checkpoints exactly.
The reverse-order run reproduced the avoided pack writes. These are
repeats on the same workload with matched stores.

## Global placement and churn

The mixed materialized store contains Coreutils, HTTPX, Parquet, SST and
tensor histories. C2's four-stage archive finish used 5,103,616 allocated
bytes. About 4.99 MB was pack file data and 2,553 bytes was pack allocation
slack in the matched trial. The inventory includes namespace, journal,
grouped authority, pack and temporary paths.

Reconstruction ordering alone matched the mixed baseline allocation.
Restartable dictionary packs took 5,222,400 bytes in locality order and
5,230,592 in reconstruction order. The latter took 7.76 CPU seconds to
repack versus 0.31 for the default. Fresh-process 4 KiB p95 was 16.94 ms
default, 20.50 ms with locality blocks, and 26.70 ms with reconstruction
ordering and blocks. These passes inherited the host page cache.

The four churn cycles combined mutations, deletions, history pruning,
full repack, compaction and GC. At the final cycle, both default and
reconstruction placement occupied about 2.63 MB. Blocks improved the
single-client p95 on that cycle and had higher parallel-repeat p95.
The mixed and churn results support retaining the established default.

## Verification

Every measured store was reopened and read back exactly. Repeated 4 KiB
reads include one first pass and one four-client pass. Pack integrity was
deep-verified. Focused tests covered unchanged raw and mixed packs,
compressible replacement, corruption refusal, duplicate sources, existing
repack and archive crash cuts, dictionary damage and concurrent online
relayout. The new path performs a read-only proof at a decision point.

Use `geometry.py --help` for the C2/C6 binaries, C5 corpus, C1 tensor
bridge and the two trial orders. The [raw traces](results/) include measured
write blocks, wall time, CPU, allocation and range distributions. C7
continues with bounded new-version discovery over verified index data.
