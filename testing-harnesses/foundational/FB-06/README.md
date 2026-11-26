# FB-06 · Exact locator generations

**Partial.** `zeros3 locator-build -store DIR` writes a
disposable generation from the offline store's validated sealed
pack indexes. `serve -locator-generation` uses it on startup.
Default opens still scan the canonical pack indexes.
`serve -locator-tags` loads a compact bucket table and the next
64 digest bits per record. It reads a checked row on each tag
hit and compares all 32 digest bytes. A failed row read rebuilds
from the original pack indexes. The legacy exact profile remains
available without a sidecar.

The generation is tied to the store ID and sorted tier and pack
inventory, including pack size, modification time, body ID,
index bounds, record count and index CRC. It stores full digests
and locator records in ordered checksummed pages. The namespace
journal and its checkpoint keep their own authority. A maintenance
pass refreshes authoritative pack indexes before making retirement
decisions. The file can be removed at any time while the store is
offline.

The focused fixture compared exact object bytes on cache hits
and after mismatched inventory, checksum, length and missing-file
fallbacks. A separate process stopped before rename and the
previous generation reopened with its original bytes and objects.
The compact profile rejected 1,000 forced equal-tag requests.
Coverage includes lookup and batch paths, CAS presence, publisher
dedup checks, payload and range reads, GC usage and native
negotiation. Twelve new pack publications stayed within the
four-level bound. After true retirement, the old sidecar was
ineligible and a fresh open verified both surviving objects.

At 16,777,216 generated records on local overlay storage, tags
opened in 124 ms warm and 159 ms after file-cache eviction advice.
Canonical open took 4.42 s. Retained Go heap was 9.006 B/record for
tags and 55.256 B/record for canonical. Process RSS was about
160 MiB and 913 MiB respectively. The 128 packs use real encoded
payloads of eight bytes each. The optional sidecar itself occupied
1.09 GB and took 10.95 s to build in this run.

Six pairs of 192 native PUTs reused every sealed chunk. Median
throughput was 149.17 MiB/s canonical and 141.80 MiB/s with tags.
The median paired ratio was 0.964, with individual ratios ranging
from 0.66 to 1.71. Cold-file throughput under payload cache pressure
requires broader qualification. Builds currently run through the
explicit offline command. Budgeted background refresh and
infrastructure qualification remain open.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
