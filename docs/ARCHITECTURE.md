# ZeroS3 architecture

ZeroS3 is an S3-compatible object store whose logical storage model is based on
content identity rather than whole-object blobs.

This document explains the internal model and the invariants that let S3
compatibility, deduplication, snapshots, delta transfer, packed storage, and
physical tiering share one storage substrate.

For current maturity and format versions, see [../STATUS.md](../STATUS.md).

## System model

ZeroS3 has three layers:

```text
APPLICATION SURFACE
  ordinary S3                  ZeroS3-aware clients
  PUT/GET/multipart            sync/replicate/repair
            \                    /
             +------------------+
                     |
                     v
LOGICAL CONTENT LAYER
  deterministic CDC
       -> SHA-256 chunk identity
       -> immutable manifests
       -> current/history/snapshot roots
                     |
                     v
PHYSICAL STORAGE LAYER
  loose CAS or immutable pack-v1
       -> locality-aware order
       -> optional per-record DEFLATE
       -> hot / warm / cold placement
       -> GC / repack / rebalance
```

The defining rule is that manifests describe logical chunks, not pack
locations. Physical layout can change without changing object identity or the
S3-visible object.

## Logical object model

A visible object resolves through the current namespace to an immutable
manifest:

```text
bucket/key
   |
current root
   |
manifest UUID
   |
   +-- ordered logical chunks
   +-- total length
   +-- whole-object SHA-256
   +-- ETag
   +-- content type
   +-- user metadata
```

Overwriting an object publishes a new manifest and root. Existing manifests are
never edited in place.

This immutable-root model is also reused by retained history, snapshots, forks,
and bundle export.

## Content-defined chunking

CDC v1 uses deterministic Gear/FastCDC-style boundaries:

```text
minimum: 16 KiB
target:  64 KiB
maximum: 256 KiB
```

Chunk boundaries depend on nearby content instead of fixed offsets. Local
insertions or deletions therefore tend to disturb only a bounded region before
the chunker re-synchronizes with unchanged data.

Each logical chunk is identified by:

```text
SHA-256(uncompressed logical bytes)
+ declared logical length
```

That identity is independent of compression, packing, tier placement, or
replication.

## Namespace authority

The CAS does not determine which objects are visible.

Current buckets, objects, and persistent multipart state are reconstructed from
an append-only checksummed visibility journal. Physical chunks or manifests may
exist without being visible through S3.

This separation is central to crash safety. A failed request may leave
unreachable immutable data, but it must not publish a partial object.

## Write path

The common logical write path is:

```text
request body
   |
streaming CDC
   |
   +--> object SHA-256 / ETag / request checksum
   +--> ordered logical chunk references
   +--> physical CAS publication
   |
immutable manifest
   |
journal/current-root commit
```

The final namespace commit is the logical visibility boundary.

### Loose CAS path

Small, size-unknown, and several internal transfer paths publish new chunks
through the grouped loose-CAS mechanism.

Chunks are staged, fsynced with bounded concurrency, deduplicated again before
publication, renamed into their shard directories, and made durable with the
required directory fsyncs.

Chunks can therefore become reusable before the final object commit while the
object itself remains invisible.

### Direct pack ingest

Known-size PutObject, UploadPart, and final multipart completion of at least
64 MiB can write new chunks directly into immutable hot packs.

New chunks are appended in first logical occurrence order. Full packs are
finalized, verified, durably published, and then added to the in-memory
locator. A final new-content tail below about 8 MiB is stored through the loose
CAS path instead of creating a very small pack.

Direct packs are raw by default because the online compression experiment
showed a large throughput and read-speed cost for adaptive DEFLATE. Compression
is therefore left to physical maintenance paths where appropriate.

## Failure behavior during writes

Physical content can be published before the request's final namespace commit
without making the object visible.

If a request later fails because of a checksum mismatch, conditional conflict,
body error, or process interruption, already-published chunks or packs may
remain unreachable. They are safe to reuse and can later be reclaimed through
normal reachability-based maintenance.

ZeroS3 does not try to roll back immutable CAS content because another request
may already have reused it.

## Read path

Reads begin with the current root and immutable manifest, then resolve each
logical chunk to an available physical copy:

```text
bucket/key
   |
manifest
   |
ordered logical chunks
   |
physical locator
   |
   +--> loose hot copy
   +--> hot packed copy
   +--> warm packed copy
   +--> cold packed copy
   |
SHA-256 verification
   |
HTTP response
```

A physical location is never trusted as proof of correctness. The logical
digest is verified before bytes are accepted.

If one packed copy is corrupt and another valid copy exists, normal copy
selection can fall back to the valid copy.

## Packed CAS

Pack-v1 groups many logical chunks into one immutable file. A pack stores
record metadata, payloads, an index, and integrity information.

Pack representation remains entirely physical, so object manifests never
contain:

- pack IDs;
- offsets;
- compression codecs;
- tier names.

This makes repack, compression, locality changes, and tier moves transparent to
the logical object model.

### Locality

Offline compaction defaults to first-reference locality order instead of digest
order. Shared chunks are stored once at their first deterministic reference.

Repack and rebalance preserve relative physical order for surviving records so
routine maintenance does not discard locality.

### Coalesced reads

When consecutive manifest chunks are physically contiguous in one pack, the
reader combines them into bounded run reads. Each logical record is still
decoded and SHA-256 verified independently.

If a record in a pack fails verification, further coalesced runs from that pack
are disabled for the request and ordinary fallback selection resumes.

### Compression

Pack records support raw and DEFLATE payloads. Adaptive compression keeps a
compressed record only when it saves enough space to justify the representation.

Compression changes physical bytes only; logical chunk identity and manifests
remain unchanged.

## Physical locator

Packed chunk lookup is reconstructed from immutable pack indexes at store open.
The locator is a compact sorted structure with prefix narrowing rather than a
large Go map entry per digest.

It can represent duplicate physical copies and tier identity. Correctness still
comes from the logical digest check, so the locator is reconstructible
performance metadata rather than a source of truth.

Durably renamed packs are safe even if the process stops before the runtime
locator is updated; the next open discovers them from the pack roots.

## Roots and reachability

ZeroS3 derives liveness from authoritative roots instead of maintaining a
persistent chunk refcount database.

Current root categories include:

- visible objects;
- retained history;
- active multipart uploads;
- immutable snapshots.

The common reachability scan is reused by verification, GC, packed live/dead
accounting, tier policy, rebalance, and structural inspection.

If an authoritative root cannot be interpreted safely, destructive GC fails
closed rather than guessing that referenced content is dead.

## History, snapshots, and forks

### Retained history

Overwrites and deletes can retain the previous object root. History supports
zero-copy restore and explicit pruning.

History is not the AWS S3 Versioning API. Pruning retires logical roots; GC and
repack handle physical reclamation separately.

### Snapshots

A snapshot captures immutable object roots for a bucket/prefix by copying
metadata references rather than payload.

Snapshots remain independent GC roots and can later be restored, inspected,
diffed, or exported as portable bundles.

### Forks

A fork republishes an existing namespace into another namespace in the same
store. Because the CAS is shared, existing payload does not need to be copied.

After publication, the destination is an ordinary independent namespace rather
than a permanently linked child.

## Portable bundles

Portable bundles contain logical storage state, never physical pack/tier state.

A full `.zs3b` contains one exact snapshot descriptor, all referenced
manifests, and every unique logical chunk once.

A delta `.zs3d` contains the complete target metadata but omits payload for
chunks already present in one exact base snapshot.

Imported targets become ordinary snapshots in the destination store; the
binary formats are specified in [../BUNDLE_FORMAT.md](../BUNDLE_FORMAT.md).

## ZeroS3-native transfer and repair

The native protocol exposes logical chunk identity to compatible clients.

A typical delta transfer is:

```text
discover endpoint
   |
build or fetch logical chunk plan
   |
negotiate missing hashes
   |
transfer only missing chunks
   |
commit ordinary destination object
```

Bulk v2 batches the same logical chunks to reduce HTTP request count without
exposing pack representation.

Peer repair reuses logical chunk identity as well. Candidate bytes fetched from
a configured peer are independently SHA-256 verified before publication. The
wire contract is specified in [ZEROS3_PROTOCOL.md](./ZEROS3_PROTOCOL.md).

## Physical tiers

Packed copies can live in hot, warm, or cold roots. Tier placement remains a
physical concern and is not exposed as AWS S3 StorageClass.

Tier policy is evaluated over logical roots, and a shared chunk inherits the
hottest requirement among all roots that reference it.

For example, a chunk referenced by both a current object and old history stays
hot while the current reference exists. Once only cold-eligible roots remain,
rebalance can move it colder.

## Maintenance

The maintenance commands all operate on the same logical/physical separation.

- **compact** packs live loose chunks without changing manifests.
- **gc** removes unreachable loose chunks and fully dead packs.
- **repack** rewrites selected partly-dead packs with verified live records.
- **tier rebalance** converges physical placement toward content-aware tier
  policy.

Old physical copies are removed only after required replacement content is
durably published and verified.

Several destructive physical-maintenance operations currently require
exclusive store ownership.

## Durability model

The recurring publication pattern is:

```text
stage bytes
fsync file
rename into place
fsync containing directory
publish higher-level reference
```

The exact steps vary by structure, but the rule remains the same: lower-level
immutable data becomes durable before a higher-level reference can make it
visible.

The namespace journal commit is the final acknowledgement boundary for ordinary
object mutations.

## Concurrency model

One server process owns a store for normal operation.

In-process synchronization protects namespace commits, pack-state publication,
grouped loose-CAS publication, snapshots, and format upgrades. The filesystem
store lock separates live serving from exclusive maintenance.

ZeroS3 is not a distributed multi-writer system.

## Why the design stays compact

Most higher-level features are compositions of a few primitives:

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

logical identity independent of physical placement
  -> packs
  -> compression
  -> locality
  -> tiers
  -> repack
  -> rebalance
```

This capability density is deliberate. New features should preferably extend
these mechanisms or introduce one reusable primitive rather than create a
parallel storage model.

## Security boundary

ZeroS3 currently provides SigV4 authentication, optional TLS, content-integrity
verification, bounded parsing, and explicit version checks.

It does not currently provide IAM/STS, policy/ACL evaluation, KMS-backed
encryption, multi-tenant isolation, or distributed consensus.

See [../SECURITY.md](../SECURITY.md) and
[OPERATIONS.md](./OPERATIONS.md) for deployment guidance.

## Related documentation

| Document | Purpose |
|---|---|
| [../README.md](../README.md) | overview and quick start |
| [../STATUS.md](../STATUS.md) | maturity and format policy |
| [../S3_COMPAT.md](../S3_COMPAT.md) | S3 contract |
| [OPERATIONS.md](./OPERATIONS.md) | operator procedures |
| [ZEROS3_PROTOCOL.md](./ZEROS3_PROTOCOL.md) | content-native protocol |
| [../BUNDLE_FORMAT.md](../BUNDLE_FORMAT.md) | portable snapshot artifacts |
| [BENCHMARKS.md](./BENCHMARKS.md) | measured behavior |