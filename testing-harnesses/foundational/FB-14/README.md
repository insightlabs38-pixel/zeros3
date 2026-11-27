# FB-14 · Chunk deltas

**In progress.** The Go research harness tries one verified base per
chunk. It uses the native Gear table, anchor matcher, XD instruction
decoder, DEFLATE compressor and pack writer. It exact-deduplicates
first, then consults a bounded similarity table and at most three
neighboring positions in the previous revision. It prices four CDC
sizes and keeps independent compressed records when a patch misses
its minimum savings.

The reopened research packs verify their body, index and whole-object
digests. The alternate codec exists only in the test harness. Ingest
pricing includes its base digest and tag, native pack and index
framing and binary manifests. Retention pricing includes live deltas,
pin-only bases and any newly independent records needed after prune.
Native optimized controls are being measured on the same fixtures.
No format integration has been selected.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
