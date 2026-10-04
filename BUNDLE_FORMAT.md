# ZeroS3 portable snapshot bundles

Two artifacts move one immutable snapshot between stores. All integers are
big-endian; the magic decides the format, the file extension is only a convention.

| | full bundle | delta bundle |
|---|---|---|
| format | **full bundle format v1** | **delta bundle format v1** (not "bundle v2") |
| extension / magic | `.zs3b` / `ZS3BNDL1`, footer `ZS3BEND1` | `.zs3d` / `ZS3DLTA1`, footer `ZS3DEND1` |
| carries | descriptor, all manifests, every unique chunk once | descriptor, **all** manifests, every unique chunk as a record, payload only for chunks the exact base lacks |
| standalone | **yes** — the self-contained archival primitive | **no** — depends on one exact base snapshot |
| `inspect -verify` needs | nothing | `-base-store DIR` holding the base |

## Full bundle v1 (`.zs3b`)

A full bundle is an offline, self-contained, self-verifying copy of **one** immutable
snapshot: its descriptor, every manifest it references, and every unique logical
chunk those manifests need. It carries logical content only — never pack IDs,
offsets, loose paths, tiers, StoreID, pack codec state or journal records.
All integers are big-endian. A bundle is one stream, in this order:

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
| 32 | SHA-256 of the **uncompressed** chunk — the chunk's identity |
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

Descriptor: store's canonical order. Manifests: UUID order. Chunks: SHA-256 order.
Nothing depends on physical layout, so exports of one unchanged snapshot are
byte-identical for a given compression mode (always for `off`; for `auto` with the
same Go DEFLATE implementation).

### Import and publication guarantee

Import is offline and needs exclusive store ownership. It verifies the stream
while publishing missing chunks through the grouped durable CAS path (existing
loose/packed/warm/cold copies are reused after a verified read; a present but
unreadable copy is repaired from the verified payload). Manifests are staged and,
only after the whole bundle (including footer hash and EOF) verified, published
under their original UUIDs (an existing identical manifest is reused, a differing
one aborts the import). The snapshot descriptor is published **last**: it is the
atomic logical-publication boundary. A failed or interrupted import leaves at most
unreachable immutable chunks/manifests and never a visible snapshot; the ordinary
namespace, journal and `FORMAT.json` are never modified. Re-importing the same
bundle is idempotent; the same SnapshotID with a different descriptor is refused.

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

The artifact still describes the **whole target**: the descriptor frame, *every*
target manifest (also those the base has — metadata is small, and carrying it lets
the file prove the target structure, chunk inventory and lengths by itself) and one
chunk record for *every* target unique chunk. Only payload bytes are omitted. A
deleted object needs no record: the target descriptor simply omits it.

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

The base is named by ID **and** by the hash of its stored frame; a store whose
snapshot of that ID has any other bytes is not the base. Target descriptor and
manifest records are exactly as in the full bundle (descriptor frame verbatim,
manifests in UUID order, same record layout and checks).

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

* **Not self-contained.** Export (`zeros3 bundle export -store DIR -snapshot
  TARGET -base-snapshot BASE -out update.zs3d`) needs both snapshots in the same
  store (`BASE == TARGET` is valid: all metadata, zero payload). Export never falls
  back to a full bundle; the reuse ratio is reported and the caller decides.
  Export a full `.zs3b` of the target for an archival, base-independent copy.
* **Inspect.** `bundle inspect -in FILE` auto-detects the format. For a delta it
  reports the header-level view (*parsed*), including the base ID and descriptor
  hash, without needing the base. `-verify` requires `-base-store DIR`; without it
  the command fails: payload omission can only be verified against the declared base.
  Verification resolves the base (exact frame hash, verified manifests, inventory),
  checks every base-reference membership and length, reads every referenced base
  chunk through the CAS, verifies every payload, footer and EOF (*verified*).
* **Import.** `bundle import -store DIR -in FILE` needs no extra flag; the base is
  embedded. The base is resolved from the header **before any payload is read**:
  a missing base, a different descriptor frame, a damaged base manifest or an
  unreadable base-referenced chunk fails the import and publishes nothing. A delta
  carries no payload for base-referenced chunks, so it **cannot repair** a damaged
  base: repair the base (e.g. re-import its full bundle) and retry. Payload chunks
  go through the same grouped CAS path as a full import (existing valid copies are
  reused — even unrelated ones — and corrupt copies repaired from the verified
  payload); manifests keep their UUIDs; the target descriptor is published **last**.
  The namespace, journal, history, tier policy and `FORMAT.json` are untouched, and
  re-importing is idempotent.
* **Chains.** The imported target is an ordinary independent snapshot and can be the
  base of the next delta. Each delta names exactly one base; there is no chain
  metadata and no automatic search: `S0 → S1 → S2` must be imported in that order,
  and `S1 → S2` before `S0 → S1` fails because `S1` is absent.
