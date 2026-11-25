# A · Frozen B10 physical frontier

B10 was rebuilt from dcefbcb with Go 1.27.0. The replay uses the original
C1 immutable revision keys and retained corpus bytes. This includes the
original SST bytes, which the pinned producer needs captured fixups to regenerate byte for byte.
Inventory is the sum of allocated inode blocks. Optimizer bytes are a
recompression model for reachable components and authority records.

| Family | CDC after GC | Full archive after GC | Full archive default maintenance | Full archive stronger finalization |
|---|---:|---:|---:|---:|
| lodash | 1,036,288 | 4,694,016 | 3,739,648 | 507,904 |
| Parquet | 2,191,360 | 2,625,536 | 2,625,536 | 712,704 |
| SST | 11,337,728 | 4,546,560 | 4,546,560 | 3,878,912 |
| tensor | 2,457,600 | 2,207,744 | 2,207,744 | 344,064 |
| composite | 42,364,928 | 37,285,888 | 37,285,888 | 28,295,168 |

Lodash replaced the original pack with a canonical graph. The optimizer
predicted 411,014 bytes of live components and records. The old 955,489-byte
pack held mostly dead records and was rewritten by default repack. About
3.65 MB of newly live loose chunks remained because default compact defers
batches below 8 MiB. Using a 1 MiB target let those chunks pack and compress.

Parquet's model predicted 599,398 bytes after packing. Default repack did
not select the remaining packs. A 100% live threshold with recompression
realized most of the plan. Under identical strong finalization, generic
deltas retained 1,097,728 bytes and CDC retained 1,306,624 bytes. The full
graph's advantage survives physical realization. SST's matching full and
delta plans persist at 3,878,912 bytes. Deep candidates cost CPU there.

The finalized composite CDC lane retains 33,902,592 bytes. Finalized
archive reduces that by 16.5%. The additional reduction from GC-only
archive to 28,295,168 bytes comes from B10's existing pack tools.
Fixed namespace and tier files, pack boundaries, allocation rounding and
loose recipe tails explain why directory allocation differs from plan cost.

The dominant time is candidate generation. On fresh composite and SST
archives, the generator took 9.58 s and 10.76 s. Search took 0.011 s and
0.014 s. Reoptimization generated candidates even when it applied nothing.
Cross-key discovery already finds useful bases. The selected archive trace
retains C1's original S3 object and revision semantics.

C2 engineering priorities are a reproducible archive finish operation,
incumbent protection for generic-only search, bounded candidate analysis,
and request-local reconstruction reuse. Deep candidates stay available.
