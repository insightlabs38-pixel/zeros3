# ZeroS3 operations

This guide covers routine operation, backup, recovery, upgrades, maintenance,
and incident handling for a single-node ZeroS3 store. The underlying storage
model is described in [ARCHITECTURE.md](./ARCHITECTURE.md), while
[../STATUS.md](../STATUS.md) defines the current deployment limits and format
policy.

## Deployment model

A ZeroS3 store is currently:

- single-node;
- owned by one serving process for normal writes;
- protected by a filesystem lock against conflicting exclusive maintenance;
- recoverable through immutable content, manifests, journal state, snapshots,
  and portable bundles.

There is no distributed failover or multi-writer consensus.

## Before using real data

Use credentials that are not the public README examples:

```sh
export AWS_ACCESS_KEY_ID='replace-me'
export AWS_SECRET_ACCESS_KEY='replace-me-with-a-long-secret'
export AWS_REGION='us-east-1'
```

ZeroS3 currently supports one static SigV4 identity. It does not provide IAM,
STS, ACLs, or a policy engine.

Use TLS when traffic can cross an untrusted network. ZeroS3 can terminate TLS
with Go's HTTP/TLS server, but it does not automate certificate issuance or
renewal.

Choose a persistent store path whose filesystem, backup, and failure behavior
you understand. Warm and cold tier roots may live on separate mounts, but all
configured roots remain part of one logical store.

## Starting and stopping

Start a store:

```sh
./zeros3 serve -store /srv/zeros3
```

The default listen address is loopback. Configure non-loopback serving only
after setting real credentials and, where appropriate, TLS.

For normal shutdown, send SIGINT or SIGTERM and let the graceful drain finish.
Abrupt termination is tested on selected paths, but it is not the recommended
administrative stop procedure.

## Health checks

Use the lightweight structural check for routine validation:

```sh
./zeros3 verify -store /srv/zeros3
```

Use deep verification when you need to rehash live content:

```sh
./zeros3 verify -store /srv/zeros3 -deep
```

Typical reasons to run a deep check include:

- suspected corruption;
- storage-device trouble;
- validating a restored store;
- checking important data before destructive maintenance.

For lifecycle/integrity diagnostics:

```sh
./zeros3 doctor -store /srv/zeros3
```

For storage accounting:

```sh
./zeros3 stats -store /srv/zeros3
```

Machine-readable JSON forms are available where documented by the CLI.

## Online and offline operations

The practical rule is:

> Stop `zeros3 serve` before any command that requires exclusive store
> ownership.

The live server holds a shared store lock. Destructive physical maintenance
requires the exclusive lock and should fail rather than race normal writes.

Common online operations include S3 reads/writes, snapshots, sync, replication,
repair, and read-only inspection.

Common offline/exclusive operations include bundle import, compact, destructive
GC, repack, tier initialization/movement/rebalance, and history pruning.

## Backup

### Full portable backup

A portable backup starts with an immutable snapshot and exports it as a
self-contained `.zs3b` bundle.

1. Create a snapshot.
2. Export it to a full bundle.
3. Verify the bundle.
4. Copy the bundle to independent storage.
5. Periodically test restoration into a fresh store.

Create and list snapshots:

```sh
./zeros3 snapshot create   -endpoint http://127.0.0.1:9000   s3://important-bucket

./zeros3 snapshot list   -endpoint http://127.0.0.1:9000
```

Export a full bundle:

```sh
./zeros3 bundle export   -store /srv/zeros3   -snapshot SNAPSHOT_ID   -out backup.zs3b
```

Verify it:

```sh
./zeros3 bundle inspect -in backup.zs3b -verify
```

A full bundle contains the snapshot descriptor, all referenced manifests, and
all unique logical chunk payloads required by the snapshot.

### Incremental bundle chains

A `.zs3d` delta contains complete target metadata but omits payload already
available from one exact base snapshot.

```sh
./zeros3 bundle export   -store /srv/zeros3   -snapshot TARGET_ID   -base-snapshot BASE_ID   -out update.zs3d
```

Verify a delta against its base store:

```sh
./zeros3 bundle inspect   -in update.zs3d   -verify   -base-store /srv/zeros3
```

A delta is not self-contained. Keep periodic full bundles instead of depending
on an indefinitely long chain of deltas.

A practical pattern is:

```text
full S0
  -> delta S0->S1
  -> delta S1->S2
  -> new full checkpoint
```

## Restore

Import a full bundle into a fresh or compatible destination store:

```sh
./zeros3 bundle import   -store /srv/zeros3-restored   -in backup.zs3b
```

Bundle import publishes the snapshot descriptor last. An interrupted import may
leave unreachable chunks or manifests, but it must not expose an incomplete
snapshot.

After import, start the destination server and restore the snapshot into an
explicit live bucket/prefix.

For a delta chain, import the exact base first, then apply deltas in order:

```text
full S0
delta S0->S1
delta S1->S2
```

A delta cannot repair a damaged base-referenced chunk because it intentionally
contains no payload for that chunk. Repair or restore the base before retrying.

## Filesystem-level backups

A stopped store can also be protected by a filesystem snapshot or copy.

If you use this method, treat all configured roots as one store. Do not copy
only `store/packs/` while omitting journal state, manifests, snapshots, loose
chunks, tier roots, `FORMAT.json`, or tier metadata.

Portable `.zs3b` bundles remain the simpler logical backup format when moving
data between machines.

## Upgrades

Before upgrading:

1. stop the existing server cleanly;
2. record the current version or commit;
3. keep a backup appropriate to the value of the data;
4. preferably keep a verified full bundle for critical state.

Then install the newer binary, open the store, and run:

```sh
./zeros3 doctor -store /srv/zeros3
```

or:

```sh
./zeros3 verify -store /srv/zeros3
```

Only after that should you enable features that may raise `FORMAT.json`.

A newer binary can open supported older store levels. A feature that requires a
newer reader raises the store format before publishing state that the old
reader could misinterpret.

### Downgrades

Do not edit `FORMAT.json` to force a downgrade.

If a newer feature has raised the store to a format an older binary does not
understand, refusal is the intended behavior. Use logical export or transfer
into another compatible store instead.

## Reclaiming space

Logical root retirement and physical deletion are separate operations.

### Retire history

Review retained versions first. If older history is no longer needed, run a
dry-run prune:

```sh
./zeros3 versions prune   -store /srv/zeros3   -bucket my-bucket   -keep-last 3
```

Apply only after reviewing the plan:

```sh
./zeros3 versions prune   -store /srv/zeros3   -bucket my-bucket   -keep-last 3   -apply
```

Pruning removes history roots, not chunk bytes.

### Garbage collection

Inspect unreachable data:

```sh
./zeros3 gc -store /srv/zeros3
```

Apply:

```sh
./zeros3 gc -store /srv/zeros3 -apply
```

GC removes unreachable loose chunks and fully dead packs. If authoritative
liveness cannot be established safely, destructive GC refuses to proceed.

### Repack

Partly-live packs are immutable. Repack creates verified replacements
containing the live records.

Dry run:

```sh
./zeros3 repack -store /srv/zeros3
```

Apply:

```sh
./zeros3 repack -store /srv/zeros3 -apply
```

## Compaction and compression

Compaction moves live loose chunks into immutable packs:

```sh
./zeros3 compact -store /srv/zeros3
```

Locality layout is the default.

Large known-size uploads may already be directly packed, so compaction is most
useful for small-object workloads, unknown-size paths, bundle/repair/bulk
imports, and older loose stores.

Direct-ingest packs are raw by default; adaptive compression is available when
creating or rewriting packs, but current `repack` only rewrites selected
partly-dead packs. It is not a general command for recompressing every healthy
raw pack.

## Tier operations

Inspect configured physical tiers:

```sh
./zeros3 tier status -store /srv/zeros3
```

### Initialize a new or replacement root

If a configured warm/cold mount is intentionally empty:

```sh
./zeros3 tier init -store /srv/zeros3 -tier warm
```

or:

```sh
./zeros3 tier init -store /srv/zeros3 -tier cold
```

Initialization creates the structural tier marker. It does not restore missing
data.

If the failed device held the only copy of a live chunk, verification will
still report that content missing.

### Move and rebalance

`tier move` and `tier rebalance` are dry-run by default. Review their plans
before using `-apply`.

A tier move publishes and verifies the destination copy before removing the
source.

Rebalance derives desired placement from logical roots. If shared chunks have
different tier requirements, the hottest requirement wins.

Use `-min-misplaced-percent` when you want to avoid rewriting an almost-correct
pack because only a small fraction of its live records are misplaced.

## Replacing a failed tier device

A conservative recovery sequence is:

1. stop the server;
2. attach or mount the intended replacement device;
3. run `tier init` only if that root is genuinely new and empty;
4. run `verify`;
5. repair or restore any missing live chunks;
6. run `verify -deep`;
7. resume service only after the store validates.

The tier marker checks are intentionally fail-closed so an accidentally
unmounted device is not mistaken for an empty tier.

## Corruption recovery

Detect suspected corruption with:

```sh
./zeros3 verify -store /srv/zeros3 -deep
```

`zeros3 repair` can fetch required logical chunks from an explicitly configured
ZeroS3 peer. The local store hashes candidate bytes before publication, so the
peer supplies availability rather than integrity authority.

After repair, run deep verification again. If no peer has the content, restore
it from a verified backup or bundle.

## Snapshots and multipart roots

Snapshots and active multipart uploads are reachability roots.

Deleting a snapshot removes the snapshot root, not payload immediately. If you
want the resulting unused data reclaimed, follow snapshot deletion with the
appropriate history/GC/repack workflow.

Aborted large multipart uploads can leave dead direct packs. Ordinary GC
reclaims them.

## Monitoring

ZeroS3 currently exposes CLI/JSON operational state rather than a metrics
daemon.

Use:

- `stats -json` for storage accounting;
- `doctor -json` for lifecycle and integrity information;
- `verify` for explicit validation;
- normal system/process tooling for CPU, memory, disk, and I/O.

There is no built-in Prometheus endpoint in the current feature set.

## Incident response

For unexplained read failures or suspected storage damage:

1. avoid destructive maintenance;
2. preserve logs and, if possible, a filesystem snapshot or copy;
3. stop the server if physical storage is unstable;
4. run structural `verify`;
5. run `verify -deep` when feasible;
6. identify another physical copy, peer, or bundle containing the required
   logical digest;
7. repair or restore;
8. verify again before returning to destructive maintenance.

## Tested recovery boundary

The test suite covers deterministic crash injection across many publication
boundaries, process restart during multipart work, real abrupt termination on
selected ingest/import paths, retry convergence, and corruption/truncation
handling for several persistent formats.

It does not claim comprehensive coverage of hardware power loss, faulty
controller caches, kernel/filesystem corruption, or simultaneous loss of
several devices.

Backups should account for those limits.

## Maintenance summary

| Goal | Command or primitive | Notes |
|---|---|---|
| structural check | `verify` | read-only |
| full content rehash | `verify -deep` | more expensive |
| lifecycle diagnostic | `doctor` | read-only |
| estimate dead data | `gc` | dry-run |
| delete unreachable content | `gc -apply` | exclusive |
| reclaim partly-dead packs | `repack`, then `-apply` | exclusive |
| pack loose content | `compact` | exclusive |
| retire old history | `versions prune`, then `-apply` | exclusive |
| capture namespace state | `snapshot create` | online |
| portable full backup | snapshot + `.zs3b` | self-contained |
| incremental snapshot transfer | `.zs3d` | exact base required |
| repair live content | `repair` | explicit peer |
| inspect physical tiers | `tier status` | physical placement |
| converge tier policy | `tier rebalance` | dry-run first |

## Security

Current deployment security is intentionally narrow: SigV4, one static
credential pair, optional TLS, and content-integrity verification.

ZeroS3 does not currently provide IAM/STS, per-user authorization, bucket
policies/ACLs, KMS-backed encryption, or multi-tenant isolation.

See [../SECURITY.md](../SECURITY.md) for the security policy and reporting
process.

## Related documentation

| Document | Purpose |
|---|---|
| [../README.md](../README.md) | quick start |
| [../STATUS.md](../STATUS.md) | maturity and format versions |
| [../S3_COMPAT.md](../S3_COMPAT.md) | exact S3 behavior |
| [ARCHITECTURE.md](./ARCHITECTURE.md) | storage model |
| [ZEROS3_PROTOCOL.md](./ZEROS3_PROTOCOL.md) | content-native protocol |
| [../BUNDLE_FORMAT.md](../BUNDLE_FORMAT.md) | full and delta bundle formats |