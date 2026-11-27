# FB-12 · Record entropy coding and delta matcher

**In progress.** The harness starts from the archived X6 Go LZ and range
coder. A research record adds one explicit version byte and a payload
CRC32C. Decode checks the production 256 KiB record bound and the expected
whole-record SHA-256. The independent LZMA1 decoder reproduced the
archived vector. Codec 6 remains conditional.

The production engine exported distinct CDC v1 records and both native
DEFLATE payload forms. The harness measured allocated files at the
production record, index, pack header and footer geometry. Each alternative
retains the native incumbent when the coded output costs more. The
same independent records went through liblzma xz -9e.

| Corpus | DEFLATE-6 allocated | Deep fast | Deep optimal | xz -9e |
|---|---:|---:|---:|---:|
| X9 source tree | 2,445,312 | 2,252,800 | 2,158,592 | 2,162,688 |
| TypeScript source package | 4,558,848 | 4,136,960 | 3,878,912 | 3,895,296 |
| SQLite and event data | 344,064 | 253,952 | 241,664 | 245,760 |
| Held-out HTTPX | 155,648 | 143,360 | 135,168 | 135,168 |
| Independent entropy | 4,202,496 | 4,202,496 | 4,202,496 | 4,202,496 |

The initial candidate missed the 10% fast / 15% optimal size gates on
X9, TypeScript and HTTPX. Deep optimal decoded 43.1 MiB/s on X9 versus
85.6 for xz, and 69.8 MiB/s on TypeScript versus 115.9. Model selection,
decoder hot paths and XD program measurements remain in progress.
Current blocked records remain available for range-oriented data.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
