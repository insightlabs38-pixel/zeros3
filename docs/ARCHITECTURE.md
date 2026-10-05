# ZeroS3 architecture

ZeroS3 is an S3-compatible object store whose logical storage model is
content-addressed rather than whole-object-blob-addressed.

This document explains the internal model and the invariants that let features
compose without changing the ordinary S3 surface.

For current maturity and format versions, see [../STATUS.md](../STATUS.md).
For the exact S3 contract, see [../S3_COMPAT.md](../S3_COMPAT.md).

## Design summary

The architecture has three layers:

```text
APPLICATION SURFACE
  S3 / SigV4
  ZeroS3-native sync/replication/repair
                |
                v
LOGICAL CONTENT LAYER
  CDC
    -> SHA-256 chunk identities
    -> immutable object manifests
    -> journal-backed current roots
    -> history / snapshots / multipart roots
                |
                v
PHYSICAL STORAGE LAYER
  loose chunk files
  immutable pack-v1 files
    -> locality-aware order
    -> optional per-record DEFLATE
    -> hot / warm / cold locations
    -> GC / repack / rebalance
```

The central separation is:

> **Logical chunk identity is independent of physical chunk placement.**

A manifest never says "read pack X at offset Y." It says that the object needs a
logical SHA-256 chunk of a declared length. A reconstructible locator chooses a
physical copy at read time.

That choice is what allows physical layout to evolve without rewriting logical
object metadata.

## Logical object model

An ordinary object is represented by an immutable manifest.

Conceptually:

```text
object key
   |
   v
current namespace root
   |
   v
manifest UUID
   |
   +-- ordered chunk A
   +-- ordered chunk B
   +-- ordered chunk C
   |
   +-- total length
   +-- whole-object SHA-256
   +-- ETag
   +-- Content-Type
   +-- user metadata
```

The ordered chunk list is the authoritative recipe for reconstructing object
bytes.

The manifest is immutable after publication. An overwrite creates a new
manifest/root rather than editing the old manifest in place.

## Content-defined chunking

ZeroS3 uses deterministic Gear/FastCDC-style content-defined chunking.

Current CDC v1 bounds:

```text
minimum: 16 KiB
target:  64 KiB
maximum: 256 KiB
```

Chunk boundaries depend on content, not fixed offsets.

This matters for revision-heavy data. A localized insertion or deletion tends
to perturb only nearby chunks before the chunker re-synchronizes with the
unchanged suffix.

A fixed-size chunker would shift every later boundary after an insertion.

CDC format/version is part of the persistent compatibility contract. The same
bytes under the same CDC version must produce the same logical chunk sequence.

## Chunk identity

Each logical chunk is identified by:

```text
SHA-256(uncompressed logical chunk bytes)
```

plus its declared logical length.

Chunk identity never changes because of:

- loose versus packed storage;
- compression;
- pack order;
- hot/warm/cold placement;
- replication;
- snapshots;
- bundles.

Every successful physical read is rechecked against the logical digest.

This means the in-memory physical locator is a performance index, not an
authority for content correctness.

## Namespace authority

The current bucket/object namespace is not inferred from files in the CAS.

It is reconstructed from an append-only visibility journal.

The journal is:

- checksummed;
- sequence-ordered;
- replayed at store open;
- the authority for current bucket/object roots and persistent multipart state.

Physical chunks and manifests can exist without being visible through S3.

That property is important for crash safety: a failed ingest may leave immutable
unreachable material, but it must not make a partial object visible.

## Write pipeline

The common logical write pipeline is:

```text
request body
   |
   v
streaming CDC
   |
   +--> object SHA-256 / ETag / request checksums
   |
   +--> ordered logical chunk refs
   |
   +--> physical CAS publication
   |
   v
immutable manifest
   |
   v
journal/current-root commit
```

The final root commit is the logical visibility boundary.

### Small / unknown-size writes

The general CAS path stages new loose chunks in bounded groups.

The grouped durable publication introduced for the loose path:

1. stages temporary files;
2. fsyncs chunk files with bounded concurrency;
3. rechecks deduplication;
4. renames durable candidates into the chunk shard;
5. fsyncs created directories;
6. releases the publication barrier.

A chunk can therefore become reusable before the final object commit, but the
object itself is not visible until its manifest/root commits.

### Direct pack ingest

Known-size PutObject, UploadPart, and final multipart completion of at least
64 MiB can use the direct-pack path.

New chunks are written in first logical object occurrence order directly into
immutable hot pack-v1 files.

A full pack is:

1. written to a staging file;
2. finalized with its normal pack index/footer;
3. fsynced;
4. verified;
5. renamed into the hot pack root;
6. parent directory fsynced;
7. added to the in-memory locator.

The same pack writer emits pack-v1 bytes for both online direct ingest and
offline compaction.

A final new-content tail below about 8 MiB is stored through the ordinary loose
CAS path rather than creating a pathological tiny pack.

Direct packs are raw by default. Compression was intentionally kept off the
online default because the measured write/read cost outweighed the storage
benefit for the default path.

## Failure model during ingest

A write can fail after some chunks or packs have already been published.

Examples:

- request checksum mismatch;
- conditional PUT failure;
- client body failure;
- process termination;
- final namespace conflict.

The safe outcome is:

```text
physical immutable data may remain
manifest/root for failed object is not committed
object is not visible
unreachable material is later reclaimable
```

ZeroS3 deliberately does not try to roll back already-published immutable
content transactionally. Another concurrent write may already have reused it.

This turns failed-write cleanup into ordinary reachability/GC instead of a
second rollback subsystem.

## Read pipeline

A read begins with the authoritative current root, loads the immutable
manifest, and reconstructs requested logical ranges from chunk references.

Conceptually:

```text
bucket/key
   |
   v
current root
   |
   v
manifest
   |
   v
logical chunk sequence
   |
   v
physical locator
   |
   +--> loose hot copy
   +--> hot packed copy
   +--> warm packed copy
   +--> cold packed copy
   |
   v
verify SHA-256
   |
   v
HTTP response
```

A corrupt physical copy is not trusted merely because the locator points to it.
The reader can fall back to another valid physical copy.

## Packed CAS

Pack-v1 groups many logical chunks into one immutable file.

A pack contains:

- fixed format header;
- chunk records;
- fixed-size index entries;
- integrity metadata/footer.

Records store logical digest, logical length, physical payload length, codec,
and offset metadata needed to reconstruct the physical locator.

The pack format is independent of object manifests.

## Locality-aware packing

Digest order is excellent for deterministic indexing but poor for sequential
object reads.

ZeroS3 therefore defaults compaction to locality order.

The locality planner walks live roots in a deterministic priority order and
ranks each unique candidate chunk by first reference.

For current storage policy that walk begins with current objects and also
covers multipart, snapshots, and retained history.

Shared chunks are stored once at their first ranked position.

Maintenance operations preserve source physical order for survivors so locality
does not disappear during routine repack/rebalance.

### Coalesced packed reads

When consecutive manifest chunks occupy physically contiguous records in one
pack, the read path combines them into bounded run reads instead of performing
one file open/pread per chunk.

Every logical record is still independently decoded and SHA-256 checked.

If one record fails, the request stops using coalesced runs from that damaged
pack and returns to ordinary physical-copy selection.

## Packed compression

Pack records support:

- codec 0: raw;
- codec 1: DEFLATE.

Adaptive compression stores a record compressed only when the result saves at
least 1/16 of its logical size.

Compression changes physical bytes only.

The chunk's logical SHA-256, object manifest, ETag, snapshot, bundle, and
replication identity are unchanged.

Online direct packs are raw by default. Offline compaction/repacking can create
compressed records.

## Physical locator

Packed chunk lookup is rebuilt from immutable pack indexes at open.

The current locator is a compact sorted representation with prefix narrowing
rather than one large Go map.

The locator can represent:

- primary packed location;
- duplicate/fallback physical copies;
- hot/warm/cold tier identity.

Pack publication updates the locator through immutable state replacement under
the pack lock.

A crash after durable pack rename but before the runtime locator update is safe:
the next open rebuilds the locator from the pack directory.

## Root categories and reachability

Physical liveness is derived from logical roots rather than reference counters.

The major live root categories are:

- current object roots;
- retained historical versions;
- active multipart uploads;
- immutable snapshots.

A common reachability walk feeds:

- GC;
- packed live/dead accounting;
- tier policy;
- rebalance;
- structural inspection.

This avoids maintaining a separate persistent per-chunk refcount database.

A broken authoritative root makes destructive GC fail closed rather than
guessing that referenced data is garbage.

## Retained history

When a current object is overwritten or deleted, the replaced root can be
retained as internal ZeroS3 history.

History is not AWS S3 Versioning.

It exists to preserve immutable logical roots cheaply and supports:

- listing retained versions through ZeroS3 tooling;
- zero-copy restore;
- explicit pruning;
- later physical reclamation by GC/repack.

Pruning retires roots; it does not directly delete chunk bytes.

## Snapshots

A snapshot is an immutable descriptor of current namespace roots for one
bucket/prefix.

It stores object-root metadata, not copied payload.

Snapshots:

- are independent GC roots;
- survive later mutation/deletion of live objects;
- can restore into an ordinary namespace using existing content;
- can be exported to full or delta bundles.

Snapshot creation copies current root metadata under the namespace lock, then
publishes the descriptor durably without holding that lock across slow I/O.

## Forks and structural sharing

A fork republishes an existing namespace into another destination namespace
within the same store.

Because source and destination share one CAS, negotiation finds the payload
already present.

The result is ordinary independent S3 objects whose manifests reference
already-existing chunks.

The fork has no persistent "parent" relationship after publication.

## Portable bundles

Portable bundle formats deliberately contain **logical** storage information,
not physical pack/tier state.

A full `.zs3b` includes:

- one exact snapshot descriptor;
- all referenced manifests;
- every unique logical chunk once.

A delta `.zs3d` includes:

- the complete target descriptor;
- all target manifests;
- one logical record for every target unique chunk;
- payload only for chunks absent from one exact base snapshot.

Imported targets become ordinary snapshots in the destination store.

See [../BUNDLE_FORMAT.md](../BUNDLE_FORMAT.md).

## ZeroS3-native delta transfer

The ZeroS3-native protocol exposes the logical chunk graph to aware clients.

A typical transfer is:

```text
discover endpoint
   |
describe source object / build local CDC plan
   |
negotiate logical hashes with destination
   |
transfer only missing chunks
   |
commit ordinary destination object
```

An optional bulk transport batches the same logical chunks to reduce HTTP
request count without exposing physical pack representation.

See [ZEROS3_PROTOCOL.md](./ZEROS3_PROTOCOL.md).

## Repair

Peer repair is content-addressed.

The local store first identifies missing/corrupt live logical chunks through its
own verification/reachability machinery.

For each required digest, the configured peer can provide candidate bytes.

The local side independently hashes those bytes against the requested digest
before publication.

The peer is trusted as a source of bytes, not as an integrity authority.

## Physical tiers

The logical CAS can have packed copies in:

- hot;
- warm;
- cold.

Hot includes the main pack root and loose chunks. Warm/cold can be separate
mounts under tier roots with store/tier identity markers.

Lookup preference is physical policy only. Logical object identity does not
contain a tier.

### Content-aware tier policy

Tier policy is applied to logical roots.

Each live reference requests a desired tier. A shared chunk's effective desired
tier is the hottest requested tier among all live roots referencing it.

Example:

```text
current object wants chunk A hot
old history wants chunk A cold

effective placement for A = hot
```

A history-only chunk can move cold without dragging content still needed by a
current object out of the hot tier.

This is a content-graph policy rather than an object-blob storage-class label.

## Maintenance model

### Compact

Offline compaction moves loose live chunks into immutable packs.

It never changes logical manifests.

### GC

GC computes reachability from all authoritative roots.

It can remove:

- unreachable loose chunks;
- packs containing no live records.

It does not edit a partially-live immutable pack.

### Repack

Repack replaces selected partly-dead packs with new verified packs containing
their live survivors.

Source physical order is preserved for locality.

### Tier rebalance

Rebalance computes desired chunk placement from root policy and current
physical layout.

It can:

- pack loose chunks into a target tier;
- move an entire pack when every live record needs the same new tier;
- split/rewrite mixed packs when necessary.

Every destructive old-copy removal occurs only after a verified surviving copy
exists.

## Durability invariants

The recurring durable-publication shape is:

```text
stage
  -> fsync bytes
  -> atomic rename
  -> fsync containing directory
  -> publish higher-level reference
```

The journal/root commit is the point at which an object mutation becomes
logically visible/acknowledged.

The implementation uses the same small durability vocabulary across chunks,
packs, manifests, snapshots, and format metadata rather than depending on an
embedded database transaction layer.

## Concurrency model

One server process owns a store for normal operation.

Important in-process synchronization boundaries include:

- namespace/root lock;
- pack-state publication lock;
- loose-CAS publication barrier;
- snapshot-specific synchronization;
- filesystem store lock for online versus exclusive maintenance.

Offline destructive maintenance requires exclusive ownership and cannot run
against a live serving process that holds its shared store lock.

ZeroS3 is not a distributed multi-writer system.

## Why the architecture stays compact

Many features are projections of the same underlying primitives:

```text
CDC + CAS
  -> deduplication
  -> edit reuse
  -> delta transfer

immutable manifests + roots
  -> history
  -> restore
  -> snapshots
  -> forks
  -> bundle metadata

logical identity != physical placement
  -> packs
  -> compression
  -> locality
  -> tiers
  -> repack
  -> rebalance
```

This capability density is intentional.

A new feature should preferably compose existing primitives or introduce one
general mechanism that enables several capabilities, rather than adding an
independent storage subsystem.

## Format boundaries

The current versions are summarized in [../STATUS.md](../STATUS.md).

Important rules:

- format changes are explicit;
- older unsupported readers fail closed;
- manifests stay physical-layout agnostic;
- store format is raised before state requiring the newer reader is published;
- bundle formats are external transport artifacts, not live CAS formats.

## Security and trust boundaries

ZeroS3 currently provides:

- SigV4 authentication;
- optional TLS;
- digest verification for content;
- bounded/untrusted parsing on network and bundle inputs.

It does not provide:

- IAM/STS;
- ACL/policy evaluation;
- encryption at rest/KMS;
- multi-tenant isolation;
- distributed trust/consensus.

See [OPERATIONS.md](./OPERATIONS.md) for deployment guidance.

## Further reading

- [../README.md](../README.md) — overview and quick start
- [../STATUS.md](../STATUS.md) — maturity / version policy
- [../S3_COMPAT.md](../S3_COMPAT.md) — S3 contract
- [OPERATIONS.md](./OPERATIONS.md) — operator procedures
- [ZEROS3_PROTOCOL.md](./ZEROS3_PROTOCOL.md) — content-native wire protocol
- [../BUNDLE_FORMAT.md](../BUNDLE_FORMAT.md) — portable snapshot artifacts
- [BENCHMARKS.md](./BENCHMARKS.md) — measured behavior
