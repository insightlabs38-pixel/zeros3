# FB-06 · Exact locator generations

**In progress.** `zeros3 locator-build -store DIR` writes a
disposable generation from the offline store's validated sealed
pack indexes. `serve -locator-generation` uses it on startup.
Default opens still scan the canonical pack indexes.

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
The current reader materializes the resident exact locator.
The compact tag profile and the scale and ingest gates remain open.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
