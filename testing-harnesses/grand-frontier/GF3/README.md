# GF3 · multi-source and shared phrase factoring

The native region shortlist matched the exhaustive two-source oracle on
three controlled cases. A derived phrase bank improved physical storage
on the correlated synthetic cohort. SQLite, Parquet and held-out HTTPX
still favored the current archive. Phrase factoring remains research.

## Discovery

The exact oracle verifies every native XD candidate among the null,
12 single-source and 66 ordered-by-ID pair choices. The comparison fixes
source direction and the native matcher, envelope, and 32 KiB output frames.
On the complementary case, all shortlists recovered both sources and the
49,294-byte encoded program. On the short-containment case, bottom-16
found neither source (85,274 bytes). Baseline regional counts matched
both and achieved 82,059 bytes. A 2 KiB marginal coverage bin masked a
second source (83,148 bytes). Reducing the bin to 512 bytes recovered both
in this control, with little discovery CPU change. Entropy stayed literal.
The 1.125 MiB source pool has three generated target queries. It supports
the containment failure mode and coarse-bin ablation, with insufficient
scale to justify replacing the real B7 discovery index.

## Physical factoring

Sample exact 32-byte anchors across objects, extend repeated spans strictly,
price savings using the incumbent record compressor, and reject overlapping
uses. Pack accepted phrases in 16 KiB records sorted by SHA or by subscription
mask. Compile exact byte programs over the resulting shared pages. Charge
entire referenced pages, including parts unneeded by an individual root.
Verify whole bytes and ranges after compaction and reopen.

| Family | CDC | Native single XD | Native multi XD | Full archive | Best phrase lane |
| --- | ---: | ---: | ---: | ---: | ---: |
| Controlled cohort | 581,632 | 323,584 | 315,392 | 315,392 | 303,104 |
| SQLite | 655,360 | 319,488 | 319,488 | 319,488 | 536,576 |
| Parquet | 1,253,376 | 1,019,904 | 1,019,904 | 634,880 | 901,120 |
| HTTPX source | 319,488 | 188,416 | 188,416 | 188,416 | 331,776 |
| Entropy | 8,433,664 | 8,433,664 | 8,433,664 | 8,433,664 | 8,433,664 |

The synthetic cohort saved 12,288 bytes (3.9%). Co-use ordering improved
the diagnostic sparse p95 from 0.48 to 0.42 ms, near 0.40 ms for native
archive. Both banks read about 66 KiB of packed input at p95. Three exact
pages remained after half the objects and their histories were pruned.
All three were reclaimed after the last referring histories were pruned.

For SQLite, Parquet and HTTPX, the best single allocation remained higher
even after optimistically subtracting every packed bank record byte
(401,131, 685,354, 231,163 bytes respectively, before possible pack-tail
changes). A 64/128/256 KiB bank sweep did not improve on the 384 KiB bank
in SQLite or Parquet. Small banks sometimes expand storage by discarding
already-cheap shared CDC chunks. Current fixed programs leave too many
literal compressed bytes for these histories. Better physical clustering
of the same pages cannot explain the gap. Future representation research
would have to reduce patch literals or combine with a different baseline
representation, such as structured normalized records. The larger Parquet
phrase bank was faster than minimum-space native archive on this probe,
while FB-14 already has a stronger experimental Parquet point.
[FB-14 measurements](../../foundational/FB-14/README.md)

[Design](PREREGISTRATION.md) · [Reproduction](REPRODUCE.md) · [Evidence](results/)
