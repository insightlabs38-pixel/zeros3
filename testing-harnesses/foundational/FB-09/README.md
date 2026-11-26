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

Downgrade coverage, metadata pages and additional formats are in progress.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
