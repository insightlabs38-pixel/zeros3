# FB-09 · Binary metadata and canonical pages

**Partial.** `serve -binary-manifests` selects a bounded binary
manifest encoder for eligible new records. It persists the
`manifestbin` feature before publishing the first binary manifest.
The legacy encoder is still the default and the new reader handles
mixed stores.

The binary record includes 36-byte ordered references and a
domain-separated chunk-list ID. The journal still records the
digest of the complete manifest bytes. The whole-object SHA-256
remains the identity for object contents.

The independent golden has 273 bytes for two chunk references.
The codec rejected every truncation of the golden and a corrupt
chunk-list ID. Mixed ordinary reads, a packed range read, deep
reachability and exact bundle export and import passed. A pre-feature
binary refused the typed store at open. The current binary verified it.

`namespace-checkpoint -pages` and `serve -metadata-pages` build a
derived visible-key archive at the existing checkpoint cadence. Leaf
and branch pages use an independent digest and a per-store secret
for key partitioning. `namespace-pages` verifies that every reachable
row exactly matches the bound namespace-v4 checkpoint. Damaged or
stale archives cannot change reads or reachability in the store.

| Generated visible keys | Flat v4 bytes | Page archive bytes |
|---:|---:|---:|
| 1 million | 77,152,061 | 76,580,545 |
| 10 million | 779,774,897 | 773,829,603 |

At one million keys, changing 1,000 evenly spaced records produced
6,067,866 bytes of previously unseen pages and framing, 7.86% of
the flat visible-key copy. That measures reuse available to a future
snapshot writer. The checkpoint sidecar still physically writes the
whole archive. Five insertion orders yielded one root. When every
key missed the content boundary, the largest page was 65,334 bytes
against a 65,536-byte cap.

The scale comparison used generated rows with native encoders.
It measured the visible-key table on both sides.

`legacy-copy -store SOURCE -out DESTINATION` reconstructs a separate
current-only store at `DESTINATION/store`. It verifies each chunk,
object hash and metadata before publishing the destination. The actual
pre-FB09 binary verified copies from both v1 and J2 originals and
refused the typed originals. The copier requires an offline source and
refuses retained history, snapshots, pending uploads and adopted body
authority. It preserves bucket timestamps, manifest IDs and object
metadata. The destination has a new store identity and journal sequence.

Authoritative page reachability, durable snapshot page reuse and the
journal record format remain open for FB-10 and FB-11. The million-object
snapshot and default format gates remain owner decisions.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
