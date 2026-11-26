# FB-09 · Binary metadata and canonical pages

**In progress.** `serve -binary-manifests` selects a bounded binary
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

The scale comparison generated realistic byte shapes through the
native encoders and excluded history and uploads from both sides.
Old-reader downgrade and snapshot publication remain open, along with
authoritative page reachability and the journal record work in FB-10.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
