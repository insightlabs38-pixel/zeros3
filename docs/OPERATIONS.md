# ZeroS3 operations

This guide is for running and maintaining a ZeroS3 store safely.

It focuses on tasks and failure boundaries rather than storage internals. For
architecture, see [ARCHITECTURE.md](./ARCHITECTURE.md). For the exact S3
surface, see [../S3_COMPAT.md](../S3_COMPAT.md).

## Operational model

A ZeroS3 store is currently:

- single-node;
- owned by one serving process for normal writes;
- protected by a filesystem lock against destructive offline maintenance;
- recoverable from immutable content/manifests plus the visibility journal;
- explicitly maintained with `verify`, `doctor`, snapshots, bundles, GC,
  repack, and tier commands.

There is no distributed failover or multi-writer consensus.

## Before using real data

### Use your own credentials

The credentials shown in the README are public examples.

Set real values before exposing ZeroS3 beyond loopback:

```sh
export AWS_ACCESS_KEY_ID='replace-me'
export AWS_SECRET_ACCESS_KEY='replace-me-with-a-long-secret'
export AWS_REGION='us-east-1'
```

ZeroS3 currently has one static SigV4 credential pair. It does not implement
IAM, STS, ACLs, or a policy engine.

### Use TLS for untrusted networks

ZeroS3 can terminate TLS directly using Go's standard HTTP/TLS server.

Configure a certificate and key with the server TLS flags when traffic can
cross an untrusted network.

ZeroS3 does not automate certificate issuance or renewal.

### Pick a persistent store directory

Do not treat the default `./zeros3-data` path as ephemeral if the data matters.

Use a directory backed by storage whose persistence and backup behavior you
understand.

Warm/cold tier roots may be separate mounts, but the store remains one logical
unit. Losing the only physical copy of a live logical chunk is data loss unless
another trusted source or bundle can repair it.

## Starting and stopping

### Start

```sh
./zeros3 serve -store /srv/zeros3
```

The default listen address is loopback. Configure a non-loopback address only
after setting real credentials and, where appropriate, TLS.

### Stop

Use SIGINT or SIGTERM and allow graceful shutdown to complete.

Do not routinely use SIGKILL as an administrative stop mechanism.

Selected mutation paths are tested against abrupt process termination, but a
clean stop remains the normal operational procedure.

## Routine health checks

### Fast structural check

```sh
./zeros3 verify -store /srv/zeros3
```

This validates store structure and logical references without the full
whole-content rehashing cost of deep verification.

### Deep verification

```sh
./zeros3 verify -store /srv/zeros3 -deep
```

Use this when:

- validating important backups/restores;
- investigating suspected corruption;
- after storage-device trouble;
- before destructive maintenance when confidence matters more than speed;
- periodically for high-value stores if the cost is acceptable.

### Lifecycle diagnostic

```sh
./zeros3 doctor -store /srv/zeros3
```

`doctor` reports integrity/lifecycle information without changing the store.

Use `-deep` when you want the diagnostic to include deep verification.

### Storage accounting

```sh
./zeros3 stats -store /srv/zeros3
```

Use the JSON forms of operational commands where you need machine-readable
automation.

## Online versus offline operations

The important rule is simple:

> If a command needs exclusive store ownership, stop `zeros3 serve` first.

The server holds a shared store lock while running. Destructive maintenance
requires the exclusive lock and should fail rather than race the live server.

Typical online/client-facing operations:

- S3 reads/writes;
- snapshot create/list/show/delete/restore;
- sync/replicate/repair;
- probe;
- read-only inspection.

Typical offline/exclusive store operations:

- bundle import;
- compact;
- destructive GC apply;
- repack apply;
- tier initialization/movement/rebalance;
- history pruning.

A dry-run command may still intentionally require exclusive ownership when its
plan depends on a stable physical store.

## Backup strategy

ZeroS3 snapshots and bundles separate namespace capture from portable backup.

### Recommended portable backup flow

1. Create an immutable snapshot while the server is running.
2. Stop the server if needed for the chosen export/maintenance workflow.
3. Export the snapshot as a full `.zs3b`.
4. Verify the bundle.
5. Copy the bundle to independent storage.
6. Periodically test import into a fresh store.

Create a snapshot:

```sh
./zeros3 snapshot create   -endpoint http://127.0.0.1:9000   s3://important-bucket
```

List snapshots:

```sh
./zeros3 snapshot list   -endpoint http://127.0.0.1:9000
```

Export a self-contained bundle:

```sh
./zeros3 bundle export   -store /srv/zeros3   -snapshot SNAPSHOT_ID   -out backup.zs3b
```

Verify it:

```sh
./zeros3 bundle inspect -in backup.zs3b -verify
```

A full bundle is the portable archival primitive. It includes the snapshot
descriptor, every referenced manifest, and every unique logical chunk once.

See [../BUNDLE_FORMAT.md](../BUNDLE_FORMAT.md).

## Incremental backup chains

A delta bundle stores the target snapshot's full metadata but omits payload
already present in one exact base snapshot.

Example:

```sh
./zeros3 bundle export   -store /srv/zeros3   -snapshot TARGET_ID   -base-snapshot BASE_ID   -out update.zs3d
```

A delta is **not self-contained**.

To verify it:

```sh
./zeros3 bundle inspect   -in update.zs3d   -verify   -base-store /srv/zeros3
```

For disaster recovery, keep periodic full bundles rather than relying on an
unbounded chain of deltas.

A reasonable pattern is:

```text
full S0
  -> delta S0->S1
  -> delta S1->S2
  -> ...
  -> new full checkpoint
```

The exact retention cadence is workload-dependent.

## Restoring a portable bundle

Use a fresh or compatible destination store.

Import is offline and does not populate the ordinary live namespace directly.

```sh
./zeros3 bundle import   -store /srv/zeros3-restored   -in backup.zs3b
```

After import, the snapshot exists in the destination store.

Start the destination server and restore the snapshot into an explicit live
bucket/prefix using the snapshot restore command.

A bundle import publishes the snapshot descriptor last. An interrupted import
may leave unreachable chunks/manifests, but must not expose an incomplete
snapshot.

Re-import is designed to converge/idempotently reuse content.

## Delta bundle restore

To import `.zs3d`, the destination must already contain the exact declared base
snapshot.

Import the chain in order:

```text
full S0
delta S0->S1
delta S1->S2
```

A delta cannot repair a damaged base-referenced chunk because it intentionally
contains no payload for that chunk.

If verification/import reports a damaged base, repair or restore the base first.

## Filesystem-level backups

A stopped store can also be protected with a filesystem-level snapshot/copy if
the underlying filesystem/storage system provides one.

Treat all configured tier roots as part of the store.

Do not copy only `store/packs/` while ignoring:

- the visibility journal;
- manifests;
- snapshots;
- loose chunks;
- warm/cold tier roots;
- `FORMAT.json`;
- tier markers/policy metadata.

For portable logical recovery across machines, `.zs3b` remains the simpler
artifact contract.

## Upgrade procedure

ZeroS3 uses explicit persistent format versions.

Before an upgrade:

1. stop the existing server cleanly;
2. record the currently running binary/version/commit;
3. make a backup appropriate to the value of the data;
4. preferably keep a verified full snapshot bundle for critical state.

Then:

1. install/build the newer binary;
2. open the store with the newer binary;
3. run `doctor` or `verify`;
4. start serving;
5. only then enable features that may advance `FORMAT.json`.

A newer binary can open supported older store levels.

A feature that requires a newer reader raises the store format before
publishing incompatible state.

See [../STATUS.md](../STATUS.md).

## Downgrade

Do not edit `FORMAT.json` by hand to force a downgrade.

If a feature has raised the store to a format an older binary does not
understand, that older binary is expected to refuse the store.

Use logical transfer/export into another compatible store if you must move data
to an older implementation.

## Reclaiming space safely

Logical root retirement and physical deletion are intentionally separate.

### Step 1: decide what may stop being live

Current objects are always roots.

Other roots can include:

- retained history;
- active multipart uploads;
- snapshots.

If old versions should remain recoverable, do not prune them.

### Step 2: prune history when appropriate

History prune is dry-run unless `-apply` is supplied.

Example pattern:

```sh
./zeros3 versions prune   -store /srv/zeros3   -bucket my-bucket   -keep-last 3
```

Review the plan, then apply deliberately:

```sh
./zeros3 versions prune   -store /srv/zeros3   -bucket my-bucket   -keep-last 3   -apply
```

Pruning retires history roots. It does not synchronously delete payload.

### Step 3: inspect GC

```sh
./zeros3 gc -store /srv/zeros3
```

GC is dry-run by default.

If the live-root scan is not trustworthy, destructive GC refuses to proceed.

### Step 4: apply GC

```sh
./zeros3 gc -store /srv/zeros3 -apply
```

GC removes unreachable loose chunks and fully dead packs.

### Step 5: repack partly-dead packs

Partly-live packs are immutable and cannot be edited in place.

Inspect repack:

```sh
./zeros3 repack -store /srv/zeros3
```

Apply only after reviewing the plan:

```sh
./zeros3 repack -store /srv/zeros3 -apply
```

Repack publishes verified replacement packs before removing old ones.

## Compaction

Compaction converts loose live chunks into immutable packs.

With the server stopped:

```sh
./zeros3 compact -store /srv/zeros3
```

Locality layout is the default.

Large known-size uploads may already be directly packed, so compaction is most
useful for:

- small-object workloads;
- unknown-size upload paths;
- bundle/repair/bulk-created loose chunks;
- older stores created before direct packing.

Compaction changes physical layout only.

## Compression

Packed records can use adaptive DEFLATE.

Online direct packs are raw by default because the measured CPU/read tradeoff
was unfavorable for the default ingest path.

Compaction can encode compressible loose data with the pack compression policy.

Current `repack` rewrites selected partly-dead packs; it is not a general
"recompress every healthy raw pack" command.

Do not assume that setting `-compression auto` will rewrite fully-live healthy
raw packs.

## Tier operations

ZeroS3 supports physical hot/warm/cold pack roots.

Warm/cold may be separate devices/mounts.

### Inspect

```sh
./zeros3 tier status -store /srv/zeros3
```

### Initialize a replacement/new tier root

If a configured tier device is absent/empty and the store refuses to open,
initialize the structural tier marker only after mounting the intended
replacement storage:

```sh
./zeros3 tier init -store /srv/zeros3 -tier warm
```

or:

```sh
./zeros3 tier init -store /srv/zeros3 -tier cold
```

`tier init` does **not** restore data.

If the failed device held the only physical copy of a live chunk, verification
will still report missing content.

### Move packs

`tier move` is dry-run by default. Review the plan before `-apply`.

A move copies into the destination tier, verifies and durably publishes the new
copy, then removes the source.

### Rebalance by content policy

`tier rebalance` is also dry-run by default.

It computes desired physical placement from live-root policy and the hottest
requirement among every reference to each chunk.

Use `-min-misplaced-percent` when you want to defer expensive whole-pack
rewrites caused by only a few misplaced records.

## Replacing a failed warm/cold device

A safe recovery sequence is:

1. stop the server;
2. mount/attach the intended replacement storage;
3. run `tier init` only if the tier root is genuinely new/empty;
4. run `verify`;
5. repair/restore any missing logical chunks;
6. run `verify -deep`;
7. only then resume normal operation.

Never use an empty tier root as evidence that the old data was unnecessary.

The marker checks are intentionally fail-closed to catch an accidentally
unmounted device.

## Corruption recovery

### Detect

```sh
./zeros3 verify -store /srv/zeros3 -deep
```

### Repair from a trusted peer

`zeros3 repair` can fetch missing/corrupt live logical chunks from an
explicitly configured ZeroS3 peer.

The local side independently SHA-256 verifies candidate bytes before
publication.

The peer is therefore trusted for availability, not integrity.

After repair:

```sh
./zeros3 verify -store /srv/zeros3 -deep
```

If no peer contains the required content, restore it from a verified backup or
bundle.

## Snapshot lifecycle

Snapshots are GC roots.

Deleting a snapshot removes the root descriptor, not chunk bytes directly.

After deleting snapshots that are no longer needed:

1. review retained history;
2. run GC dry-run;
3. apply GC if appropriate;
4. repack partly-dead packs if worthwhile.

## Multipart cleanup

Active multipart uploads are live roots.

Abort abandoned uploads rather than leaving them indefinitely.

Large UploadPart requests can create direct packs. Aborting the upload can
therefore leave fully-dead packs; ordinary GC reclaims them.

## Monitoring today

ZeroS3 currently provides CLI/JSON observability rather than a metrics daemon.

Use:

- `stats -json` for storage accounting;
- `doctor -json` for lifecycle/integrity state;
- `verify` for explicit validation;
- process/system metrics for CPU, memory, disk usage, and I/O.

There is no built-in Prometheus endpoint in the current feature set.

## Incident checklist

For unexplained read failures or suspected storage damage:

1. avoid destructive maintenance;
2. preserve logs and, if possible, a filesystem snapshot/copy;
3. stop the server if physical storage is unstable;
4. run structural `verify`;
5. run `verify -deep` if feasible;
6. identify whether another physical copy/peer/bundle contains the missing
   digest;
7. repair or restore;
8. verify again;
9. only after a clean live-root scan consider GC/repack.

## Disaster-recovery boundaries

Tested behavior includes:

- deterministic crash injection at many durable-publication boundaries;
- process restart during multipart work;
- real abrupt process termination on selected ingest and bundle-import paths;
- retry/convergence behavior;
- corruption/truncation handling for several persistent formats.

Not comprehensively tested/claimed:

- hardware power loss during every mutation path;
- faulty controller write caches;
- filesystem/kernel corruption;
- simultaneous loss of several independent devices;
- distributed network partitions (ZeroS3 is not a distributed store).

Plan backups accordingly.

## Maintenance decision table

| Goal | First command / primitive | Notes |
|---|---|---|
| check structure | `verify` | read-only |
| fully rehash content | `verify -deep` | read-only, more expensive |
| inspect lifecycle | `doctor` | read-only |
| estimate dead data | `gc` | dry-run |
| remove unreachable loose/fully-dead packs | `gc -apply` | offline/exclusive |
| reclaim partly-dead pack space | `repack` then `repack -apply` | offline/exclusive |
| pack loose content | `compact` | offline/exclusive |
| retire old object history | `versions prune` then `-apply` | offline/exclusive |
| capture point-in-time namespace | `snapshot create` | online via endpoint |
| portable full backup | snapshot + `.zs3b` | self-contained |
| incremental snapshot transfer | `.zs3d` | exact base required |
| repair live corrupted chunks | `repair` | explicit trusted peer |
| inspect tiers | `tier status` | physical placement |
| converge tier policy | `tier rebalance` | dry-run first |

## Security summary

Current deployment security is intentionally narrow.

ZeroS3 has:

- SigV4;
- one static credential pair;
- optional TLS;
- content-integrity verification.

It does not currently have:

- IAM/STS;
- per-user authorization;
- bucket policies/ACLs;
- server-side encryption/KMS;
- audit-compliance framework;
- multi-tenant isolation.

Do not infer enterprise controls that are not documented.

## Related documentation

- [../README.md](../README.md) — quick start
- [../STATUS.md](../STATUS.md) — maturity / format versions
- [../S3_COMPAT.md](../S3_COMPAT.md) — exact S3 behavior
- [ARCHITECTURE.md](./ARCHITECTURE.md) — storage model
- [ZEROS3_PROTOCOL.md](./ZEROS3_PROTOCOL.md) — content-native protocol
- [../BUNDLE_FORMAT.md](../BUNDLE_FORMAT.md) — full/delta artifact formats