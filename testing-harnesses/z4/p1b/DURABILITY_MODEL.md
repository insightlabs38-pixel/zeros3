# Z4-P1B durability and visibility model

Written before the publication path was changed; the implementation is checked against it.
Function names are those of `zeros3.go` at `b220973`.

## 1. Ordering of one small PUT (loose CAS), baseline and P1B

| # | step | baseline | P1B |
|---|---|---|---|
| 1 | stage chunk in `tmp/cas-*.tmp` (`casStage`: create, write, close) | request goroutine | same |
| 2 | chunk data verified: digest computed by the chunker over the exact bytes written | before staging | same |
| 3 | `fsync(tmp file)` (`casFlushFiles`, bounded parallel) | before any rename | same; shard-directory creation + parent fsync run **concurrently** with it |
| 4 | claim digest | global `casPubMu.Lock` for the whole publication | per-digest claim (`pubRegistry`), released after step 7 |
| 5 | `rename(tmp, chunks/aa/bb/<sha>)` | inside the lock | after the claim |
| 6 | chunk now **visible to this process** at its final path | | observers that see the file also see the claim and wait |
| 7 | `fsync(chunks/aa/bb)` = directory entry durable | inside the lock, one per call | shared with concurrent callers of the same directory (`dirSync`) |
| 8 | claim released: chunk is **reachable through the CAS index** (`casStat`/`casRead` return it) | lock released | claim released |
| 9 | manifest staged, `fsync(file)`, `rename` into `manifests/`, `fsync(manifests/)` | after step 8 | **overlaps** steps 3-8 (needs only the chunk list, not durability) |
| 10 | journal frame written + `fsync(visibility.log)` = **commit point** | `Store.mu` held across the fsync | one write + one fsync for all frames queued meanwhile (`commitQueue`) |
| 11 | in-memory namespace updated (`s.buckets`, `s.history`) | after fsync, same lock | after fsync, in journal order, same critical section |
| 12 | HTTP 200 / `ETag` to the client | after step 11 | after step 11, only for requests whose frame is covered by the successful fsync |

## 2. What each state means

* **Visible to the current process**: final path exists (after 5). Not a durability claim.
* **Reachable through the CAS index**: `casStat`/`casRead` answer "present" (after 8). Invariant: *never before the directory entry is durable.* Enforced because (a) a publisher claims the digest before the first rename, (b) every lookup that has seen the final path then checks the registry and waits for the claim, (c) a second publisher of the same digest waits for the first and re-checks, so a duplicate writer cannot acknowledge on top of a non-durable chunk.
* **Recoverable after process kill**: every file in `chunks/`, `manifests/` and the journal is either complete (data fsynced before rename) or absent; page cache survives a process kill, so ordering matters little here. Tested with real `kill -9`.
* **Recoverable after power loss**: requires data fsync before the rename that names it (step 3 < 5), directory fsync after it (7), journal fsync after both (10 > 7, 9). Rename atomicity alone is not durability and a file fsync does not make a new directory entry durable; both are separate barriers above. New shard directories: `mkdir aa` -> fsync `chunks/` -> `mkdir bb` -> fsync `chunks/aa` happen before any rename into `bb` is made durable, and a directory is trusted (cached "ready") only after its parent fsync returned.
* **Acknowledged**: step 12. Requires 3, 7, 9 and 10 for every object the request references. A chunk that already existed (dedup) was durable and claim-free when `casStat` returned it (step 8 of its writer); a chunk being published concurrently by another request makes `casStat` wait for that request's directory fsync.

## 3. Which steps may be shared or reordered (and why it is safe)

* Steps 3 and the shard-directory creation are independent (different files) -> run in parallel.
* Chunk chain (3-8) and manifest chain (9) are independent; only the journal frame depends on both -> run in parallel, join before step 10. A manifest that is durable without a journal record is unreachable garbage (GC), exactly as before after a failed commit.
* Directory fsyncs of the same directory are shared: a caller joins a round only after its own mutation (rename/mkdir/create) completed; the round's fsync starts after the join window is closed, so it covers every member. All members of a round get that round's error (a failed fsync is never assumed repeatable).
* Journal frames of several requests are written with one `WriteAt` and one `fsync`. Each request is released only after the fsync covering its frame succeeded; validation (bucket exists, `If-Match` / `If-None-Match`, previous version for history) is evaluated in queue order against the live namespace plus the earlier requests of the same batch, under `Store.mu`, which the batch leader holds across write, fsync and apply. A journal error poisons the journal (as before) and fails the whole batch; none of it becomes visible.
* **Not** done: no durability is deferred past the acknowledgement, file-data fsyncs are not removed (every chunk file and manifest file still has its own `fsync`), no `syncfs`.

## 4. Failure semantics

* A failed or panicking publisher removes the final files it created, then removes shard directories it created (only while empty), then releases its claims; waiters wake and re-check (`stat`); a waiting duplicate publishes the chunk itself.
* A failed directory fsync fails every member of that round; none of them returns success.
* A leader that panics (crash hook) fails its batch, still hands leadership to the oldest queued request, never leaves `leading` set; queue and claim maps hold only in-flight work, so nothing outlives the requests.
* The readiness cache (`shardAA`/`shardBB`, 256 + 65,536 bits) is only a fast path: a rename into a vanished directory (GC/admin removal) clears the bit and recreates the directory.

## 5. Correctness argument for the claim registry (replaces the global lock)

The global lock enforced: (I1) no observer depends on a chunk before its entry is durable; (I2) exactly one publisher decides creation of a chunk; (I3) a failed publication leaves nothing visible; (I4) shard directories are created once and trusted only when durable. Replacements: I1 = claim-before-rename + observer checks the claim *after* seeing the file (the file's existence implies the earlier claim insert, which is ordered by the registry's mutex); I2 = `tryClaim` is exclusive per digest and blocked publishers wait outside any claim (no hold-and-wait cycle); I3 = cleanup precedes release; I4 = per-directory creation claims (`dirClaims`) + readiness published only after the parent fsync. GC/compact/repack run only while the store is not served (shared lock), so they never race a publication in the same process; the in-process tests that call them with an open store hold no claims at that point.
