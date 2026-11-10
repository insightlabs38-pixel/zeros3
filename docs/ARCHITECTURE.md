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

Publication is guarded per digest, not globally: a publisher claims each digest
before its first rename and releases the claim only after the directory entry
is durable, and a lookup that finds a file whose digest is claimed waits for
the claim. Directory fsyncs of the same directory are shared by concurrent
publishers (a caller returns only after an fsync that began after its own
rename), and shard directories are created once and trusted only after their
parent's fsync. Object-root journal frames of concurrent requests are written
and fsynced together (group commit); each request is acknowledged only after
the fsync covering its frame, in journal order. See
`testing-harnesses/z4/p1b/DURABILITY_MODEL.md`.

### Direct pack ingest

Known-size PutObject, UploadPart, and final multipart completion of at least
256 KiB can write new chunks directly into immutable hot packs (one pack per
request for objects below 64 MiB; full 64 MiB packs above). A request whose
new content is below 64 KiB (below about 8 MiB for objects of 64 MiB or more)
keeps using loose chunks, and once a store holds 200,000 packs smaller objects
use loose chunks again until `compact`/`repack` merge the small packs.
`ZEROS3_DIRECT_PACK_MIB=0` disables direct packs entirely.
Publishing the first pack raises the store format to level 2, as a 64 MiB PUT
always did.

New chunks are appended in first logical occurrence order. Full packs are
finalized, verified, durably published, and then added to the in-memory
locator. A final new-content tail below about 8 MiB is stored through the loose
CAS path instead of creating a very small pack.

Direct packs are raw by default because the online compression experiment
showed a large throughput and read-speed cost for adaptive DEFLATE. Compression
is therefore left to physical maintenance paths where appropriate.

### Small-object segments

A known-size PUT below 1 MiB (and every manifest of at most 64 KiB, whatever the
object size) no longer creates files. Its new chunks and its manifest are records
of one append-only **segment** (`segments/<seq>.seg`): the bytes of a pack-v1 file
without index and footer. Concurrent requests share one `write` and one `fsync`
(group commit, no timer); a record becomes visible to readers only after the
`fsync` that covers it, and the journal frame, written afterwards, is still the
commit point. A manifest record is named by the SHA-256 of its exact bytes, the
digest the journal already records, so no new reference exists.

A segment seals at 32 MiB (or when `gc`, `repack`, `compact`, a tier operation or
a pack-locator rebuild needs a quiet store, and at a clean close from 8 MiB) by
appending the pack-v1 index and footer and publishing the file under
`packs/<body_sha256>.pack`: a sealed segment is an ordinary pack. Opening a store
re-hashes every record of the unsealed segment, cuts a torn tail (copied to
`tmp/seg-torn-*`) and continues appending. Format, protocol, recovery state
machine and crash model: `testing-harnesses/z4/p1c/{FORMAT,DURABILITY_MODEL}.md`.
`ZEROS3_SMALL_OBJECT_STORAGE=off` writes loose chunks and manifest files again;
segments already present stay readable. The first segment raises the store format
to level 11 (see `STATUS.md`).

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

An optional representation root (next section) is part of its manifest's root:
the components it names are marked live through the same scan, and a root that
cannot be read or that names missing state is a missing/corrupt issue that
blocks destructive maintenance until it is repaired or retired.

## Optional structured representations

Status: experimental, default off (`zeros3 serve -representations off|shadow|prefer`).

A representation is an optional, immutable description of how to rebuild an
object's exact bytes from SHA-256-addressed components instead of CDC chunks.
It never replaces the ordinary object:

```text
bucket/key -> journal -> immutable manifest -> ordinary CDC chunks      (authoritative)
                              |
                              +-> optional representation root (per manifest UUID)
                                      -> representation object (DAG of child objects)
                                      -> components (ordinary CAS chunks)
```

- The **journal remains the only namespace visibility boundary.** A root is
  metadata attached to an immutable manifest and can exist before the namespace
  commit (it is then an invisible orphan) without making anything visible.
  Publishing or retiring a root creates no journal record and no object version.
- **Identity.** Object SHA, representation SHA (SHA-256 of the serialized
  representation, format `ZS3R` v1), component SHA (SHA-256 of exact bytes) and
  format-native identity (CID, Merkle hash) stay distinct. Native identities are
  lookup hints; ZeroS3 SHA-256 is the truth.
- **Kinds** (a finite in-code switch, numbering fixed): 1 Concat (tar), 2 CAR,
  3 SQLite page tree, 5 Merkle snapshot. 4 (Git), 6 (Gzip) and 7 (Zstd) are
  reserved and fail as unsupported. A parent may point at a child representation
  by digest (DERIVED members, with prefix and zero-suffix framing); children are
  shared through ordinary content addressing, never reference counted.
- **Components.** A component of at most 256 KiB is one ordinary CAS chunk whose
  digest is the component SHA-256. A larger one is *virtual*: a flat descriptor
  of ordinary CDC chunks stored in the root file, so every root is independently
  readable and `compact` can pack everything.
- **Root file** `store/representations/roots/<manifest-uuid>.root` (`ZS3T` v1,
  CRC32C): manifest UUID, object SHA-256 and size, representation SHA, virtual
  component descriptors. It is written through tmp, fsync, rename and directory
  fsync, and is immutable. It is optional acceleration metadata: if it is absent,
  stale or unreadable the object is served by CDC exactly as before.
- **Liveness** follows the manifest: current, retained-history and snapshot
  roots keep their representation closure alive; the closure joins the common
  reachability scan. The native catalog is a derived, rebuildable accelerator and
  never keeps anything alive. A root whose manifest no live root names is garbage
  that `gc -apply` removes together with representation-only chunks.
- **Creation** is opt-in, deterministic and best-effort: after the CDC manifest is
  published and before the namespace commit the object is analysed (bounded work,
  bounded memory, content sniffing only), its components are written, the
  representation is reconstructed through the production decoder and compared with
  the object's SHA-256 and size, and only then is the root published. Any failure
  falls back to the plain CDC object; a PUT never fails because of it.
- **Build policy** (`-representation-build inline|deferred`, default `inline`).
  Inline is the behaviour above: the representation exists when the PUT is
  acknowledged. `deferred` acknowledges after the authoritative commit and builds
  on a bounded pool of background workers (`-representation-workers`,
  `-representation-queue`): identical publication rules, but the object is read
  through CDC until its root appears, identical content is built once and its root
  cloned, a full queue skips the build rather than blocking, workers yield to
  requests in flight (never longer than 5 s), and unfinished work is abandoned at
  shutdown (the queue is memory only; staged components are ordinary unreferenced
  content for `gc`). Components of a small object share the active segment's
  group commit; larger ones go to packs, exactly like the object's own chunks.
  See `testing-harnesses/z4/p4/DEFERRED_CONSTRUCTION.md`.
- **Reads** (`prefer`): ranges of SQLite pages, CAR sections and stored tar members
  may be served from the representation; a whole GET always uses CDC (whole
  structural reconstruction was slower than CDC for every measured kind). Everything
  is verified per component, and any failure before the first byte falls back to CDC
  in the same request. `shadow` keeps reads on CDC. Parsed roots and representation
  objects are cached across requests (immutable, content-addressed; a root is
  re-read when its file changes); every data chunk a read touches is still read and
  SHA-256 verified, and deep verification never uses the cache.
- **Store format 6** is raised just before the first root is published. Portable
  bundles (`ZS3BNDL1`/`ZS3DLTA1`) and the ZeroS3-native protocol are unchanged and
  carry only the authoritative CDC state; representations are local and can be
  rebuilt later.

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