# ZeroS3 portable snapshot bundle (`.zs3b`) — format v1

A bundle is an offline, self-contained, self-verifying copy of **one** immutable
snapshot: its descriptor, every manifest it references, and every unique logical
chunk those manifests need. It carries logical content only — never pack IDs,
offsets, loose paths, tiers, StoreID, pack codec state or journal records.
All integers are big-endian. A bundle is one stream, in this order:

```
header | snapshot descriptor | manifest records | chunk records | footer
```

## Header (36 bytes)

| off | size | field |
|----:|-----:|-------|
| 0 | 8 | magic `ZS3BNDL1` |
| 8 | 2 | format version = 1 |
| 10 | 2 | flags = 0 (any other value is rejected) |
| 12 | 4 | snapshot descriptor length (14 … 256 MiB + 14) |
| 16 | 4 | manifest count (≤ 4,000,000) |
| 20 | 8 | unique chunk count (≤ 2^25) |
| 28 | 8 | total unique logical chunk bytes (≤ count × 256 KiB) |

## Snapshot descriptor

The exact stored frame of the snapshot (`ZSS1` magic, version, canonical JSON
payload, CRC32C), unmodified; it is parsed with the store's snapshot parser.
SnapshotID, CreatedAt, source bucket/prefix, the ordered entries and each entry's
manifest UUID/SHA-256 are preserved verbatim. No new snapshot ID is ever minted.

## Manifest records (count from the header)

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

## Chunk records (count from the header)

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

## Footer (64 bytes)

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

## Determinism

Descriptor: store's canonical order. Manifests: UUID order. Chunks: SHA-256 order.
Nothing depends on physical layout, so exports of one unchanged snapshot are
byte-identical for a given compression mode (always for `off`; for `auto` with the
same Go DEFLATE implementation).

## Import and publication guarantee

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
