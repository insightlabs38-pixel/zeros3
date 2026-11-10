# Deferred representation construction (Z4-P4)

Policy knob: `zeros3 serve -representations shadow|prefer -representation-build inline|deferred [-representation-workers N] [-representation-queue M]`.
Runtime policy only; nothing is persisted, no store-format level changes, no wire or journal change. Default `inline` is the pre-P4 behaviour.

## Contract

| | `inline` (default) | `deferred` |
|---|---|---|
| Build runs | in the PUT, after the manifest is published and before the namespace commit | after the namespace commit, on a bounded worker pool |
| Representation exists when PUT returns | yes | no (read through CDC until the root is published) |
| PUT outcome depends on the build | never | never |
| Root visible | after components durable + exact proof | identical rule |
| Unfinished work after a crash/shutdown | n/a (inside the request) | abandoned; object stays CDC-only; staged components are unreachable CAS content that gc reclaims |
| CDC authority | unchanged | unchanged |

CopyObject clones an existing root (`repCloneRoot`) in both policies; in deferred mode a copy whose source has no root yet is queued like a PUT. The sync-commit handler and multipart completion queue after their commit. Bundle import (offline CLI) stays inline.

## Why an ephemeral queue

A persistent queue would need its own journal record, replay, torn-tail and version rules and a second source of truth about which manifests still deserve a build. The value it protects is optional: the object is complete and readable without it. Losing queued work costs only a missed acceleration, which a later PUT/COPY of the same content (or a bundle import) re-creates. Persisting it was therefore rejected.

## Queue

* One job per object SHA-256. A request for content already queued/running joins that job (`more`); when the build finishes each joiner receives a cloned root (small root file + same descriptors). Content built earlier in the process (last 4096 digests) is cloned without re-analysis.
* Admission: at most `-representation-queue` (default 256) pending+running jobs and 8 GiB of queued object bytes. A full queue **drops** the request (counted, logged once per 1000); it never blocks or fails the PUT. Objects that can never be represented (empty, above the analysis budget) are not queued.
* Workers (`-representation-workers`, default 1) take jobs FIFO. Before a job starts a worker waits until no S3 request is in flight (`fgGate`, counted in `ServeHTTP`), but never longer than 5 s: under sustained load builds still progress, at the cost of contending with the foreground. This is a wait on a condition variable, not a sleep poll.
* Memory per build is the existing per-kind analysis budget; the pool bounds the number of simultaneous builds. Queue memory is the job's manifest (chunk list, shared with the caller) times at most 256 jobs.

## Correctness

A job names an immutable manifest (UUID). It builds from that manifest's chunk list and publishes a root filed under exactly that UUID, whose `ObjSHA`/`ObjSize` the proof just checked against the manifest; a later overwrite of the key cannot redirect it.

Before publication the worker checks that bucket/key still names the manifest as its current version or in retained history (`repManifestLive`); otherwise the build is abandoned. The check is conservative in both directions: a false "dead" only costs a missed acceleration, a false "live" leaves an orphan root, the same state an inline build leaves when its commit fails, which `gc` reports and reclaims. Destructive maintenance (gc, repack, compact, tier move, prune) needs the exclusive store lock, which no running server coexists with, so no component or root a worker needs can be collected while it runs; no reachability pin is required, and a build that dies with the process leaves only unreferenced content.

Cancellation: `Store.Close` raises `rep.stopping`, abandons pending jobs, and running builds stop at their next source read or phase boundary (`errRepBuildStopped`); `Close` waits at most 10 s. A worker panic is recovered and counted as an optional failure; it cannot kill the process or poison the queue.

Lock order: workers hold no namespace/journal lock while analysing. `repManifestLive` takes `s.mu` briefly; `q.mu` is a leaf (enqueue under `s.mu` is allowed, nothing takes `s.mu` while holding `q.mu`).

## Crash and failure boundaries (all tested)

`TestP4DeferredCrashImagesAtEveryPhase` copies the store directory at: job start, analysis start, components staged, components flushed, proof done, format raised, before write, written, before rename, after rename, published, for both pack-resident and segment-resident components, shadow and prefer. Every image holds the acknowledged object byte-exact, passes `verify -deep`, has either no root or an exact one, and the build can be redone on it. `TestP4_RealKill_DeferredBuildsNeverHurtAcknowledgedData` SIGKILLs a real server mid-build for six rounds, then runs `verify -deep` and `gc -apply`.
