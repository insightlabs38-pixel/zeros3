# FB-11 · Shared-page snapshots and delta bundles

**In progress.** `serve -snapshot-pages` publishes snapshots as a
checked root over immutable shared metadata pages. Each descriptor
binds its page root, entry count and the SHA-256 of its exact portable
legacy frame. The feature requires an exclusive writer at activation.
Legacy snapshots still read alongside the page format.

A mixed three-snapshot store preserved the portable descriptor bytes,
deep verified all roots, and reimported an equal bundle over a paged
snapshot. A corrupt shared page prevented destructive GC. A full
portable export imported into a fresh store with legacy snapshots.

At one million generated entries, the flat frame was 227,000,199
bytes. The initial shared snapshot used 13,290 page files totaling
68,116,689 bytes and 109,953,024 allocated bytes. Updating 1,000 values
added 1,109 pages. New pages and the root descriptor allocated
9,228,288 bytes, 4.07% of a flat copy's allocation. Publication took
6.36 seconds initially and 2.32 seconds for the second snapshot on
the local overlay. Payload data were omitted from the scale fixture.

Snapshot page reclamation, changed-key delta transport and range
proof evaluation are in progress. Million-object production
qualification remains an owner decision.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
