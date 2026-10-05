# ZeroS3 portable snapshot bundles

This is the normative file-format reference for ZeroS3 portable snapshot artifacts. Both formats below are **format v1** and are independent of live CAS pack/tier layout. An incompatible future artifact change must use an explicit new version/magic rather than silently reinterpreting v1. Current project maturity/format policy is summarized in [STATUS.md](./STATUS.md).

Two artifacts move one immutable snapshot between stores. All integers are
big-endian; the magic decides the format, the file extension is only a convention.

| | full bundle | delta bundle |
|---|---|---|
| format | **full bundle format v1** | **delta bundle format v1** (not "bundle v2") |
| extension / magic | `.zs3b` / `ZS3BNDL1`, footer `ZS3BEND1` | `.zs3d` / `ZS3DLTA1`, footer `ZS3DEND1` |
| carries | descriptor, all manifests, every unique chunk once | descriptor, **all** manifests, every unique chunk as a record, payload only for chunks the exact base lacks |
| standalone | **yes**, self-contained archival primitive | **no**, depends on one exact base snapshot |
| `inspect -verify` needs | nothing | `-base-store DIR` holding the base |

## Full bundle v1 (`.zs3b`)

A full bundle is an offline, self-contained, self-verifying copy of **one** immutable
snapshot: its descriptor, every manifest it references, and every unique logical
chunk those manifests need. It carries logical content only, never pack IDs,
offsets, loose paths, tiers, StoreID, pack codec state or journal records.
A full bundle is one stream in this order:

```
header | snapshot descriptor | manifest records | chunk records | footer
```

### Header (36 bytes)

| off | size | field |
|----:|-----:|-------|
| 0 | 8 | magic `ZS3BNDL1` |
| 8 | 2 | format version = 1 |
| 10 | 2 | flags = 0 (any other value is rejected) |
| 12 | 4 | snapshot descriptor length (14 … 256 MiB + 14) |
| 16 | 4 | manifest count (≤ 4,000,000) |
| 20 | 8 | unique chunk count (≤ 2^25) |
| 28 | 8 | total unique logical chunk bytes (≤ count × 256 KiB) |

### Snapshot descriptor

The exact stored frame of the snapshot (`ZSS1` magic, version, canonical JSON
payload, CRC32C), unmodified; it is parsed with the store's snapshot parser.
SnapshotID, CreatedAt, source bucket/prefix, the ordered entries and each entry's
manifest UUID/SHA-256 are preserved verbatim. No new snapshot ID is ever minted.

### Manifest records (count from the header)

One record per distinct `ManifestUUID` in the descriptor, in ascending UUID order:

| size | field |
|-----:|-------|
| 36 | canonical lowercase UUID text |
| 32 | SHA-256 of the manifest bytes (must equal the descriptor's `manifest_sha256`) |
| 4 | manifest byte length (1 … 64 MiB) |
| n | the manifest file's exact bytes |

Each manifest must hash to its SHA-256, parse as manifest v1 (CDC v1, `sha256`),
carry its own record's UUID, list chunk references of length 1 … 262,144 whose
sum equals `total_length`. Unreferenced, missing, duplicate, out-of-order or
hash-conflicting records are rejected.

### Chunk records (count from the header)

One record per distinct chunk digest referenced by any manifest, in ascending
SHA-256 byte order:

| size | field |
|-----:|-------|
| 32 | SHA-256 of the **uncompressed** chunk; this is the chunk identity |
| 4 | logical length (1 … 262,144; must equal every manifest's length for the digest) |
| 4 | stored payload length |
| 1 | codec: `0` raw, `1` DEFLATE (RFC 1951) |
| n | payload |

Raw requires stored = logical. DEFLATE requires 0 < stored < logical; it is
decoded with a hard output ceiling of the logical length plus a one-byte probe,
must end exactly there with no trailing compressed bytes, and the SHA-256 of the
output must equal the digest. Exporters keep DEFLATE only when it saves at least
1/16 of the chunk, so `-compression off` and incompressible data stay raw.
The record sequence must equal, exactly, the sorted unique set derived from the
manifests: no missing, extra, duplicate or reordered record is valid.

### Footer (64 bytes)

| size | field |
|-----:|-------|
| 8 | magic `ZS3BEND1` |
| 8 | total stored payload bytes |
| 8 | raw record count |
| 8 | DEFLATE record count |
| 32 | SHA-256 of **every preceding byte** of the bundle |

The statistics must match the records, the hash must match, and the file must end
exactly after the hash; trailing bytes are invalid. A reader that has not checked
the footer has only *parsed* the bundle, not *verified* it.

### Determinism

The descriptor uses the store's canonical order, manifests are written in UUID
order, and chunks are written in SHA-256 order. Physical layout does not affect
the output, so an unchanged snapshot exports byte-identically for
`-compression off` and, with the same Go DEFLATE implementation, for
`-compression auto`.

### Import and publication guarantee

Import is offline and requires exclusive store ownership. Missing chunks are
published through the grouped durable CAS path. Existing loose, packed, warm,
or cold copies are reused only after a verified read; an unreadable copy can be
repaired from the verified bundle payload.

Manifests are staged under their original UUIDs. An identical existing
manifest is reused, while the same UUID with different bytes aborts the import.

The snapshot descriptor is published **last**, after the footer hash and EOF
have been verified. This is the atomic logical-publication boundary. An
interrupted import may leave unreachable immutable chunks or manifests, but it
never exposes a partial snapshot and does not modify the ordinary namespace,
journal, or `FORMAT.json`.

Re-importing the same bundle is idempotent. Reusing a SnapshotID with a
different descriptor is rejected.

## Delta bundle v1 (`.zs3d`)

A delta bundle is **target snapshot MINUS the chunks one exact base snapshot
already holds**. It works on logical identities (SHA-256 + logical length), not
on bytes of `.zs3b` files, and has no binary-patch or sub-chunk codec: CDC
already isolates unchanged content.

```
T = target unique chunks, B = base unique chunks (from the base's verified manifests)
payload set        = T - B   -> raw / DEFLATE records
base-reference set = T ∩ B   -> codec 2, logical length only, no payload
```

The artifact still describes the **whole target**. It contains the target
descriptor, every target manifest, and one record for every target unique
chunk. Manifests are included even when the base already has them so the delta
can describe and validate the complete target structure independently.

Only chunk payload bytes supplied by the declared base are omitted. Deleted
objects need no explicit record because the target descriptor simply omits
them.

```
header | target descriptor | manifest records | chunk records | footer
```

### Header (120 bytes)

| off | size | field |
|----:|-----:|-------|
| 0 | 8 | magic `ZS3DLTA1` |
| 8 | 2 | format version = 1 |
| 10 | 2 | flags = 0 (any other value is rejected) |
| 12 | 4 | target descriptor length (14 … 256 MiB + 14) |
| 16 | 4 | manifest count (≤ 4,000,000) |
| 20 | 8 | target unique chunk count (≤ 2^25) |
| 28 | 8 | target unique logical bytes (≤ count × 256 KiB, ≥ count) |
| 36 | 8 | payload chunk count (≤ target chunk count) |
| 44 | 8 | payload logical bytes |
| 52 | 36 | base snapshot ID (canonical lowercase UUID text) |
| 88 | 32 | SHA-256 of the **exact stored base descriptor frame** |

The base is identified by both snapshot ID and the SHA-256 of its exact stored
descriptor frame. A snapshot with the same ID but different descriptor bytes is
not an acceptable base.

Target descriptor and manifest records use the same representation and checks
as a full bundle: descriptor frame verbatim, manifests in UUID order.

### Chunk records

One record per target unique chunk, ascending SHA-256 order, same 41-byte layout as
a full bundle (digest, logical length, stored length, codec):

| codec | meaning | stored length |
|------:|---------|---------------|
| 0 | raw payload | = logical |
| 1 | DEFLATE payload | 0 < stored < logical, bounded decode, SHA-256 of output = digest |
| 2 | **base reference** | must be 0; no payload bytes follow |

Validation uses a delta-specific length rule (the pack rule plus codec 2). A
base-reference chunk must be a member of the base snapshot's inventory with the
same logical length; conversely a chunk the base holds must be a base reference.
A digest recorded with two lengths (target vs base) is invalid. Membership is
always against the declared base snapshot, never against whatever the destination
CAS happens to contain.

### Footer (72 bytes)

| size | field |
|-----:|-------|
| 8 | magic `ZS3DEND1` |
| 8 | total stored payload bytes |
| 8 | raw record count |
| 8 | DEFLATE record count |
| 8 | base-reference record count |
| 32 | SHA-256 of **every preceding byte** |

Counts must match the records and the header, the hash must match and the file
must end exactly after it. Output is deterministic for a fixed base, target,
compression mode and Go DEFLATE; with `-compression off` two exports are
byte-identical, regardless of loose/packed/tier layout of either snapshot.

### Dependency, verification and import

* **Not self-contained.** Export requires both base and target snapshots in the
  same store. `BASE == TARGET` is valid and produces full metadata with zero
  payload. Export never falls back to a full bundle; use `.zs3b` when an
  archival, base-independent artifact is required.
* **Inspect.** `bundle inspect -in FILE` auto-detects the format and can parse
  delta metadata without the base. Full verification requires
  `-base-store DIR`, because omitted payload can only be validated against the
  declared base. Verification checks the exact base descriptor, manifests,
  inventory, every base-reference membership and length, every referenced base
  chunk, payload records, footer, and EOF.
* **Import.** `bundle import -store DIR -in FILE` resolves the embedded base
  **before any payload is read**. A missing or changed base, damaged base
  manifest, or unreadable base-referenced chunk fails the import before target
  publication. A delta cannot repair a damaged base because it contains no
  payload for base-reference records. Payload chunks use the same grouped CAS
  import path as full bundles, manifests keep their UUIDs, and the target
  descriptor is published last. Namespace, journal, history, tier policy, and
  `FORMAT.json` remain unchanged.
* **Chains.** An imported target is an ordinary snapshot and can become the base
  of the next delta. Each delta names exactly one base, so chains such as
  `S0 -> S1 -> S2` must be imported in order. There is no automatic chain
  search.