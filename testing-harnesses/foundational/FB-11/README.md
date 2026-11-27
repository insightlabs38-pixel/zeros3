# FB-11 · Shared-page snapshots and delta bundles

`serve -snapshot-pages` publishes snapshots as a
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

`bundle export -changes-only -base-snapshot BASE` writes a v2 delta.
A 512-key fixture with three edits produced 2,419 bytes versus
445,347 for v1. It transferred two manifests and two payload chunks.
The receiver checked 510 reused chunks and reconstructed the exact
target descriptor. A zero-edit delta occupied 527 bytes. With three
fixed edits, metadata was 1,133 bytes at 1,000 keys and 1,136 bytes
at 100,000. A prior reader refused v2 and verified a full portable v1
export. Import still scans the exact base closure to verify reused
objects. The measured change-proportional result covers wire bytes.

The offline `snapshot-pages-gc` dry-run and apply use the exact union
of verified roots. A corrupt descriptor or page forbids removal.
A 12-snapshot randomized run kept all 1,135 modeled live roots and
reclaimed 13 files outside every retained tree.

**M-G1 research-only.** Current ranged GET verifies local block data.
Replication and bundles verify whole content-addressed chunks. The
review found no named remote range-proof consumer. For one aligned
4 KiB range in a 1 TiB object, the modeled proof adds 896 sibling bytes.
Retaining the complete tree costs 16 GiB at 4 KiB leaf width. A
16 KiB leaf cuts tree storage but requires sending the larger covering
block. [Model](range_proof_cost.py) · [Results](results/range-proof-model.jsonl)

Million-object production qualification remains an owner decision.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
