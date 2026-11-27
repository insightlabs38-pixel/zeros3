# FB-12 · Record entropy coding and delta matcher

The archived X6 Go range coder remains a research harness. The strongest
candidate met both allocation thresholds on TypeScript and on the SQLite
and event corpus. The gate required three corpora and decode throughput at
least as high as xz. X9 and held-out HTTPX missed both thresholds. Codec 6
stays out of the runtime.

## Native record results

The engine exported distinct CDC v1 records with native DEFLATE-6 and
4 KiB blocked DEFLATE. Each candidate competed per record against the
native incumbent. The harness measured actual allocation at native pack
geometry. The dictionary lane charges two copies of the dictionary, their
record and index overhead, and the preset descriptor. The table shows
the lowest allocation across independent and dictionary lanes for each
parser. The xz column uses the same records with liblzma xz -9e.

| Corpus | DEFLATE-6 bytes | Best fast bytes | Best optimal bytes | xz -9e bytes |
|---|---:|---:|---:|---:|
| X9 source tree | 2,445,312 | 2,236,416 | 2,134,016 | 2,162,688 |
| TypeScript source package | 4,558,848 | 4,018,176 | 3,751,936 | 3,895,296 |
| SQLite and event data | 344,064 | 253,952 | 241,664 | 245,760 |
| Held-out HTTPX | 155,648 | 143,360 | 135,168 | 135,168 |
| Independent entropy | 4,202,496 | 4,202,496 | 4,202,496 | 4,202,496 |

Branchless range updates, reusable models and overlap copies raised local
decode throughput. The smallest optimal lanes shown above sustained 56.3,
88.9, 117.9 and 96.0 MiB/s on the four compressible rows, versus 85.6,
115.9, 135.6 and 127.6 MiB/s for xz, respectively. The independent model
oracle tried three of six implemented contexts per record. Its strongest
X9, structured and held-out HTTPX lanes still fell below xz throughputs.
Entropic input retained raw records. Range-oriented objects keep the
native blocked option. Its allocation is included in the initial evidence.

The wrapper checks framing, dictionary context, decoded bounds and object
SHA-256. The original independent vector was also decoded by raw
liblzma LZMA1. The optimized decoder passed focused race tests and 24,000
malformed context cases in addition to the initial raw stream fuzz run.

## Structural and historical controls

Native C14 Parquet and SST histories went through the existing B9 and
B10 archive engines, GC, repack and compact. Deep verification and
whole/range reads passed. Parquet used 2,138,112 allocated bytes in the
CDC store and 2,543,616 in each archive lane. SST used 11,284,480 and
4,141,056, respectively. B10 made no additional gain on these histories.
On the same inputs the distinct-record DEFLATE pack went from 1,224,704
to 897,024 bytes with the optimal range lane for Parquet and from
10,305,536 to 10,207,232 for SST. Store and pack totals have different
coverage. The published B10 RubyGems and SST gains relied on deltas over
shared decoded streams, complementing the per-record experiment.
The C12 retained controls supplied TypeScript and windowed cases below.
The dictionary charge follows C13's dual-copy accounting.

## Delta matching and current-store pricing

The actual native matcher ran six base/target pairs from C12, C14 and
held-out histories. All ordinary and windowed outputs decoded byte-exact.
For TypeScript, the larger-frame lane reduced patch allocation 229,376 to
188,416 bytes, while total retained bytes declined only 0.87% after
including the base and body record. The 33 MiB windowed pair had identical
envelope length and allocation across four densities despite retained
anchor tables spanning 2–64 MiB. Each lane decoded 24 native 4 KiB reads.
The 5% total gate was missed. The existing frame sizes and anchor-density
defaults are retained.

The runtime shares the existing migration and optimizer window
configuration. Explicit current-store migration pricing can now generate
the exact aligned piece identities without writing them. The test
predicted 65,536 allocated bytes and measured exactly 65,536 after
publication. Pricing uses actual loose extents, filesystem units for
new loose files, encoded pack record extents and existing grouped-record
accounting. Shared-file tail allocation is modeled per component.

The 256 independent XD programs were also tested as a corpus at
equivalent pack framing. DEFLATE allocated 323,584 bytes. The tested
optimal range lane reached 282,624 bytes (12.7%), decoding at 41.3 MiB/s
versus 57.8 MiB/s for xz. This is a comparative record-cost measurement.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
