# ZeroS3 status

ZeroS3 is a pre-1.0, single-node, S3-compatible content-aware object store.
Current development emphasizes documentation, interoperability, and real
consumer integrations before additional core feature expansion.

This file is the compact source of truth for maturity, compatibility, supported
deployment shape, and persistent-format versions. For usage, start with
[README.md](./README.md). For the exact S3 contract, see
[S3_COMPAT.md](./S3_COMPAT.md).

## Current maturity

- **Stage:** public preview / pre-1.0.
- **Supported platform:** Linux is the validated target.
- **Implementation:** one production Go source file, `zeros3.go`.
- **Runtime dependencies:** Go standard library only; root `go.mod` has no
  `require` directives and CGO is not required.
- **Deployment model:** one ZeroS3 process owns one store for writes.
- **Availability model:** single-node; no distributed consensus, clustering, or
  automatic failover.
- **Identity model:** one static SigV4 credential pair per server.
- **S3 scope:** Core Client Profile v1, not full AWS S3 parity.
- **Content-native scope:** CDC/CAS deduplication, delta transfer, repair,
  structural history/snapshots/forks, portable snapshot bundles, packed CAS,
  physical tiers, and content-aware placement.
- **Release status:** no stable 1.0 compatibility promise yet. Persistent formats
  are explicitly versioned and fail closed when a binary cannot understand
  them.

New core capabilities should be driven primarily by real users, integrations,
or measured bottlenecks rather than speculative surface expansion.

## Good deployment fit

ZeroS3 is currently a strong fit for:

- local or self-hosted S3-compatible development storage;
- research and team artifact stores;
- model/checkpoint and dataset revisions;
- content-heavy build/cache workflows;
- applications that benefit from server-side CDC/CAS deduplication while using
  a standard S3 interface;
- ZeroS3-aware clients that can additionally use chunk negotiation and delta
  transfer;
- single-node stores where explicit offline maintenance is acceptable.

It is not currently intended as:

- a multi-node highly available object-store cluster;
- a public multi-tenant cloud service;
- a complete AWS S3 replacement;
- an IAM/STS/KMS/policy platform;
- a Windows storage server.

## Compatibility contracts

### S3

The portable S3 contract is **ZeroS3 Core Client Profile v1**.

A conforming client can rely on the bucket/object/multipart, SigV4,
presigning, range, conditional, checksum, CopyObject, batch delete, and listing
behavior documented in [S3_COMPAT.md](./S3_COMPAT.md).

`GET /_zeros3/v1/info` advertises:

- `implementation: "zeros3"`
- `core_s3_profile: 1`

`zeros3 probe -endpoint URL` distinguishes a ZeroS3 endpoint from a generic
S3-compatible endpoint.

### ZeroS3-native protocol

The content-native protocol is separate from S3. It is used by `zeros3 sync`,
`replicate`, `repair`, and other ZeroS3-aware tooling.

- logical sync protocol: **v1**
- optional bulk chunk transport: **v2**
- ordinary S3 remains the compatibility fallback where documented.

The native protocol is specified in
[docs/ZEROS3_PROTOCOL.md](./docs/ZEROS3_PROTOCOL.md).

## Persistent format versions

Persistent formats are explicitly versioned, and a binary that does not
understand a required format must refuse the store instead of guessing.

| Format | Current version | Notes |
|---|---:|---|
| CDC | 1 | deterministic Gear/FastCDC-style boundaries; 16 KiB min, 64 KiB target, 256 KiB max |
| Manifest | 1 | immutable JSON logical object description |
| Visibility journal | 1 framing | append-only, checksummed namespace authority; later record kinds remain versioned/fail-closed |
| Store format | 1..5 | feature floor recorded in `FORMAT.json`; see below |
| Pack | 1 | immutable packed CAS records and index |
| Snapshot descriptor | 1 | immutable namespace snapshot descriptor |
| Full snapshot bundle | 1 | `.zs3b`, magic `ZS3BNDL1` |
| Delta snapshot bundle | 1 | `.zs3d`, magic `ZS3DLTA1` |

### Store format levels

`FORMAT.json` advances only when a feature requires a newer reader:

| Store format | First feature requiring it |
|---:|---|
| 1 | loose CAS / base store |
| 2 | raw immutable packs |
| 3 | compressed pack records |
| 4 | persisted history-prune records |
| 5 | warm/cold physical tier roots |

The version never decreases automatically.

A store may remain at an older level indefinitely if it never uses a feature
that requires a newer one.

## Upgrade and downgrade posture

### Upgrade

Current binaries open supported older store-format levels and continue using
their existing data. A feature that requires a newer store format raises the
format **before** publishing state that an older reader could misinterpret.

Normal upgrade procedure:

1. stop the existing server cleanly;
2. keep a filesystem-level backup or verified portable snapshot bundle for
   important data;
3. start the newer binary against the store;
4. run `zeros3 doctor` or `zeros3 verify`;
5. only then use features that advance the store format.

The full upgrade workflow is documented in
[docs/OPERATIONS.md](./docs/OPERATIONS.md).

### Downgrade

Downgrade is not a supported migration mechanism.

If a newer feature has raised `FORMAT.json`, older binaries that do not
understand that format are expected to refuse the store by design.

To move data to an older or otherwise separate store, use logical export/
transfer primitives supported by both sides rather than editing
`FORMAT.json`.

## Pre-1.0 compatibility policy

Before 1.0:

- persistent formats remain explicitly versioned and must fail closed;
- an incompatible persistent-format change requires a new version;
- the Core Client Profile version must change if its portable S3 contract
  changes incompatibly;
- the ZeroS3-native protocol must advertise/version incompatible changes;
- documented CLI and JSON surfaces may still evolve before 1.0;
- no promise is made that every experimental/admin flag will be frozen.

The project should prefer additive evolution and backward-readable formats where
that stays simple, but compatibility is not allowed to force unsafe or
duplicative architecture.

## Durability and recovery boundary

ZeroS3's logical visibility boundary is the durable namespace/journal commit.
Chunks, packs, and manifests may be durably staged before that boundary; a
failed request can therefore leave unreachable immutable data, but must not
publish a partial object.

The implementation and tests cover deterministic crash injection broadly and
real process termination on selected ingest/import paths. They do **not** claim
comprehensive hardware power-loss, faulty-device, kernel-fault, or distributed
failure testing.

Operational validation can use:

```sh
./zeros3 doctor -store ./zeros3-data
./zeros3 verify -store ./zeros3-data
./zeros3 verify -store ./zeros3-data -deep
```

## Security boundary

Current ZeroS3 intentionally has a narrow security model:

- SigV4 authentication with one configured static access-key/secret pair;
- optional TLS using Go's standard HTTP/TLS server;
- no IAM, STS, ACL, bucket-policy engine, KMS, or object encryption;
- no server-side peer discovery;
- replication/repair peers are explicitly configured by the caller.

Do not expose the example credentials from the README beyond local testing.

## Documentation map

- [README.md](./README.md): project overview and quick start
- [S3_COMPAT.md](./S3_COMPAT.md): exact ordinary-S3 contract
- [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md): storage model and invariants
- [docs/OPERATIONS.md](./docs/OPERATIONS.md): running, backup, recovery, maintenance
- [docs/ZEROS3_PROTOCOL.md](./docs/ZEROS3_PROTOCOL.md): ZeroS3-aware protocol
- [docs/BENCHMARKS.md](./docs/BENCHMARKS.md): current and historical measurements
- [BUNDLE_FORMAT.md](./BUNDLE_FORMAT.md): portable full/delta snapshot formats
- [STDLIB.md](./STDLIB.md): zero-dependency implementation notes
- [testing-harnesses/README.md](./testing-harnesses/README.md): black-box validation