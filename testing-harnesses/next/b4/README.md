# B4: durable concurrent online GC and repacking

Linux amd64, Go 1.27.0, 4 vCPU shared by server, maintenance and load. Base `1285e6b`.
`zeros3 serve -maintenance gc|all` reclaims loose files, orphan roots, dead and mostly dead
packs while serving; offline `gc`, `repack` and `compact` are unchanged.

## Protocol (`zeros3.go` section 13d2)
* **Mark at T0** under `commitMu`+`mu`: pins start; namespace/history views, multipart
  parts, journal end and pack set are captured. **G1** drains writers (all but GET/HEAD);
  roots of the journal frames between T0 and G1 join the mark. Any root a tracked writer
  can see is then in the mark, those frames, or a tracked writer's pins. The scan is unlocked.
* **Pin before presence check** in the CAS batch, pack/segment sinks, manifest publication,
  sync commit/negotiate and authority closures: a dedup hit and a reclamation cannot both win.
* **Carry, condemn, swap**: live records are durably republished first; unpinned dead items
  are condemned and a later pin revives them. Pinned and revived pack records are carried
  before the doomed packs are masked out of the immutable locator under a short hold.
* **G2** drains every request that may hold an old locator or reference. Unlinks need
  the reader lease (a `journal/` flock shared opens hold) exclusively, else packs stay
  pending; a `chunks/` flock makes the maintainer the only writer. No new persistent state: a crash
  leaves redundant files the next pass removes. `POST|GET /_zeros3/v1/maintenance` (SigV4).

## Evidence (`zeros3_b4_test.go`, also `-race`)
Dead-digest reuse before condemnation, before and after the swap; in-flight PUTs across
the mark; readers of a deleted object and of an old locator; a shared reader process;
writer exclusion; missing and corrupt replacements; cancellation; history, snapshot and
open-upload roots; delta bodies whose only base is rewritten; 14 process-exit cuts from
mark to partial unlink, each reopened, deep-verified, every ack read back exactly, then two
converging passes. P9-style load (192 PUTs, 768 hashed GETs, 32 passes, segment seals): 0 errors.

## Matched latency ([raw](results/latency-under-maintenance.json))
3,000 objects (4-384 KiB, 858 MB, 16 MiB packs, ~1/3 dead), 12 closed-loop clients, 30 s per
phase (70% GET, 10% HEAD, 15% overwrite, 5% PUT), keep-versions 1, 2 maintenance workers;
every phase run twice from identical copies.

| phase | GET p99 ms | overwrite p99 | PUT p99 | ops | reclaimed / written |
|---|---:|---:|---:|---:|---:|
| idle | 9.2 / 10.5 | 25.2 / 29.3 | 25.3 / 27.2 | 87.6k / 80.1k | store grows to ~4 GB |
| pass every 2 s | 12.4 / 11.8 | 45.3 / 37.2 | 42.5 / 38.4 | 72.9k / 75.2k | 0.95 / 0.40 GB |
| back to back | 14.6 / 15.1 | 48.9 / 47.2 | 44.7 / 48.7 | 62.8k / 61.5k | 1.19 / 0.52 GB |

p99 stays within 1.1-1.9x of idle; server CPU +3-8% paced, +18% back to back; peak RSS
111-143 MB (idle 165). A scan takes ~108 ms. Longest holds (with lock waits): mark 7.0 ms,
locator swap 5.9 ms, record hold 5.9 ms. Rare 286-460 ms write maxima back to back (idle
76-109 ms) fit fsync queuing behind replacement packs. Peak disk tracked the end state;
restart to ready 155-205 ms.

## Limits
Every pass rereads all manifests; long downloads delay G2 and old writers G1 (then the
pass defers after `-maintenance-wait`); pack writes are not throttled; binaries before B4
ignore both flocks.

## B3-R follow-ups
* TypeScript 5.6.2/5.6.3/5.7.2 (registry sha1/sha512 verified): planned 23.38%, measured
  23.36% after gc/repack/compact; the planner admits the cohort ([raw](results/typescript-admission.json)).
* `zeros3 authority features [-apply]` records features committed bodies need but
  `FORMAT.json` lacks (B3 stores: `cxreach`); nothing else is rewritten.
