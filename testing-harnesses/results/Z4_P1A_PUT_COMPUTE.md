# Z4-P1A: high-performance PUT compute pipeline

Verdict: **P1A_PRODUCTION_READY** (see 13 for the qualification record and the one pre-existing failing stage).
Base `origin/main` `ffceb096865285135b0d9fa36774b75979b8aa8b`; branch `z4/p1a-put-compute`. Production diff: `zeros3.go` only (+~850/-43), standard library only, `go.mod` unchanged, **no store, journal, wire or capability-floor change**.
Evidence: [`../z4/p1a/`](../z4/p1a/README.md) (raw JSONL, scripts, profiles, `results/P1A_TABLES.md`). Method, driver, content generator and byte-exact verification are P0's `p0bench` unchanged; every run is a fresh store+process and all PUTs are replayed byte-for-byte in a fresh process.

## 1. Environment
Same VM class as P0 (4 vCPU Firecracker, ext4 on virtio, 16 GiB, Go 1.27.0), a different instance: baseline is **not** P0's number (64 MiB direct pack 140 vs P0 109 MiB/s; 1 GiB 127 vs 99; four streams 375 vs 322; CPU-s/GiB 6.6 vs 7.6). Cause: faster host/storage on this instance (fsync p50 about 0.25 ms, 64 MiB fsync about 50 ms), same code. All comparisons below are interleaved base/final runs on this instance, not against P0's numbers. The load generator shares the 4 vCPUs (about 0.3 core).

## 2. Verified baseline
`results/baseline_ffceb09.jsonl` (5 interleaved rounds) and the base column of every later set agree within 3%: 64 MiB direct pack **140** MiB/s at 0.90 core, 1 GiB **127**, 4x64 MiB **375**, SHA-256 about 40-45% of CPU, MD5 about 20%, CDC about 10%, syscalls 20-25%. P0's mechanism (one goroutine, serial passes) reproduces exactly; only the absolute scale differs.

## 3. Original pipeline: byte passes per payload byte (direct pack, one goroutine, in order)
| # | pass | cost ms/MiB | status |
|---|---|---:|---|
| 1 | socket read, CDC gear scan | 0.15 / 0.8 | needed; scan can skip the unobservable prefix |
| 2 | chunk SHA-256 (chunk identity) | 0.6 | needed |
| 3 | object SHA-256 (manifest) | 0.6 | needed, sequential state, cannot reuse chunk digests |
| 4 | MD5 (ETag) | 1.3 | needed, sequential, the slowest single stage |
| 5 | copy into bufio, pack body SHA-256 (pack id), write(2) | 0.2 / 0.6 / 1.0 | needed (id is a format field) |
| 6 | fsync, then **serial read-back**: pread, body SHA-256, per-record SHA-256 | 50 ms/pack + 0.2 / 0.6 / 0.6 | verification gate; body re-hash is redundant for raw packs (5) |
Five SHA-256 passes per byte. Critical path = sum of all of it, plus fsync and verification of the last pack, on one core. Distinct guarantees: input integrity (4 via ETag/checksum checks), chunk identity (2), object identity (3), pack identity (5), post-write verification (6: the file's payload hashes to the digest that names it), recovery-time integrity (unchanged `verify -deep`/open).

## 4. Implemented (all in `zeros3.go`)
1. **Pipelined ingest** (`ingestPipelinedTo`): the request goroutine reads into 0.75 MiB batch buffers, cuts whole chunks, and hands each batch (read-only, ref-counted) to an object-SHA goroutine, an MD5 goroutine and two chunk-digest workers; it then writes chunks **in order** through the unchanged sink. Cut sequence is bit-identical to the serial chunker (tail carried into the next buffer). 6 buffers bound memory and give backpressure; no channel can block (capacity >= buffer count). Used for direct-pack sizes and >= 32 MiB, with at most max(1, GOMAXPROCS/2) pipelines at once and never on 1 CPU. Test hooks and the sink stay on the request goroutine.
2. **Asynchronous pack writer** (`packWriter`): file write and body SHA-256 run on two goroutines fed by 8x256 KiB buffers (bufio replaced; same bytes, same sticky-error semantics); an **early background fsync every 16 MiB** so the final fsync has little left. An early fsync error is kept and fails the pack (a later fsync cannot be trusted to repeat it).
3. **Background pack publication** (`packSink` jobs): a full pack is finished (fsync, verify, rename, dir fsync, locator) while the request keeps ingesting; at most 2 in flight; `Flush` waits for all; digests of in-flight packs stay in the repeat check (a chunk is still written once); worker panics (crash hooks) are captured and re-raised on the request goroutine.
4. **Exact, parallel read-back** (`verifyPackExact`) for packs written in this process with only raw records: header, index and footer are compared with encodings rebuilt from memory and the writer's body digest; every record header is compared; every payload is read back and must hash to its digest; runs are checked on up to 4 goroutines. Every file byte is covered by exactly one check, so the file equals what the writer produced and its body digest (computed over exactly those bytes) is correct: this proves what the body re-hash proved, **one SHA-256 pass fewer**. Packs with any deflate record keep `verifyPackFile`. A mutation test flips 2,768 single bits across every region (all header/index/footer bytes, record headers, payload edges, random positions, truncations/extension): full and exact verifiers both reject every one.
5. **CDC scan**: the low mask bits depend on the last 16 bytes only, so the scan starts at min-16, is split by mask and unrolled 4 bytes/step: **1.53x** faster (1.2 -> 1.83 GB/s), cuts proven identical to the frozen v1 reference on about 60 MiB of varied data (both eof modes) and every boundary length.

## 5. Invariants (all tested)
Bytes: chunk list, sizes, object SHA-256 and MD5 equal the serial path and independent digests for sizes 0..24 MiB under 1-byte/half/data-err readers. Bounded memory: 6 batch buffers + 8 writer buffers + <=2 jobs' verify buffers; nothing scales with object size except the manifest chunk list (as before). Cancellation/errors: source error, premature EOF, mid-chunk stop, sink failure at chunk 1/2/30/200, unpublishable pack dir, corruption injected between fsync and verify (both verifiers), crash hook inside a background job (re-raised on the request, restart, exact retry): no goroutine leak, no staging file left, request fails. Durability order is unchanged (data fsync, verify, rename, dir fsync, locator, then manifest and journal), nothing is acknowledged before `Flush` has waited for every job.

## 6. Results (interleaved, median [min-max]; full table `P1A_TABLES.md`)
| scenario | base MiB/s | final MiB/s | speedup | CPU-s/GiB | cores | peak RSS MiB |
|---|---:|---:|---:|---:|---:|---:|
| 64 MiB direct pack, 1 stream | 140 [138-142] | **428 [415-443]** | **3.07x** | 6.56 -> 5.96 | 0.9 -> 2.5 | 18 -> 34 |
| 1 GiB, 1 stream | 127 [125-130] | **488 [474-500]** | **3.85x** | 7.33 -> 5.97 | 0.9 -> 2.8 | 24 -> 44 |
| 4 x 64 MiB streams | 375 [345-402] | 550 [505-565] | 1.47x | 6.61 -> 5.67 | 2.4 -> 3.0 | 30 -> 60 |
| 64 MiB, 3 revisions (similar) | 193 | 546 | 2.8x | 5.23 -> 4.66 | | 19 -> 37 |
| 64 MiB text / runs / representations=shadow | 144 / 145 / 136 | 452 / 442 / 419 | 3.1x | | | |
| 64 MiB signed payload / aws-chunked | 143 / 125 | 459 / 331 | 3.2x / 2.6x | | | |
| 64 MiB forced loose / compress=auto | 72 / 64 | 85 / 76 | 1.18x / 1.20x | | | |
| multipart 64 MiB, 1 / 4 parts in flight | 51 / 64 | 58 / 65 | 1.15x / 1.02x | | | |
| 16 MiB c1 / c4, chunked c4 | 67 / 87 / 91 | 66 / 86 / 86 | 0.98 / 1.00 / 0.95x (noise, see 8) | | | |
| 1 MiB c1 / c4, 4 KiB c1 / c8, 64 KiB c8 | 51 / 76 / 1.3 / 3.4 / 35 | 53 / 80 / 1.4 / 3.2 / 35 | 1.0x | | | |
Independent repeats of the 1 GiB and 64 MiB cells across the sets (`final_ablation`, `final_gomaxprocs`, earlier candidates) span 420-488 and 415-449 MiB/s; the base spans 119-127 and 137-141. **Not** 250 MiB/s "or the storage ceiling": the target was exceeded 1.7-2x and the device is not the limit here (dev writes = 1.003x logical, fsync 64 MiB about 50 ms).
Bytes on disk are identical (`phys_per_logical` 1.0028, `wchar` 1.003x, read-back `rchar` 2.00x = body + verify, unchanged); **device flushes per GiB drop 136 -> 20 (1 GiB)**, 160 -> 64 (64 MiB).

**GOMAXPROCS** (final/base): 1 -> 1.39x (1 GiB), 1.17x (64 MiB), CPU-s/GiB 7.7 -> 6.0 (no pipeline is started on one CPU, so it degrades to the cheaper serial path); 2 -> 2.44x / 2.06x; 4 -> 3.77x / 3.16x. 4x64 MiB: 1.21x / 1.21x / 1.48x.

## 7. Where the gain comes from (ablation of the final build; MiB/s)
| 1 GiB | 64 MiB c1 | 4x64 MiB | mechanism removed |
|---:|---:|---:|---|
| 420 | 449 | 548 | none (final) |
| 258 | 244 | 544 | hashing pipeline: **-39% / -46%** (single stream's biggest lever; nothing at 4 streams, where cores are already busy) |
| 328 | 322 | 481 | asynchronous pack writer: -22% / -28% / -12% |
| 398 | 435 | 562 | background publication: -5% / -3% (matters only with several packs per object) |
| 427 | 292 | 500 | exact verifier -> full `verifyPackFile`: 0% / **-35%** (it is the tail of the last pack) |
| 447 | 361 | 461 | early fsync off: +6% (noise) / -20% / -16% |
| 119 | 137 | 386 | baseline |
Reduced work: SHA-256 CPU **-17..-19%** (one pass in five), CDC CPU **-30%**, GC cycles 53 -> 21 and allocated bytes -23% (193 -> 148 MB per GiB); total CPU per GiB **-9% (64 MiB), -19% (1 GiB), -14% (4 streams)**. MD5 (1.3 CPU-s/GiB) and three more SHA-256 passes are required outputs and unchanged. Overlap, not work, is therefore most of the speedup; on a loaded machine the CPU reduction is what remains (4 streams: +47%).

## 8. Small objects, concurrency, GOMAXPROCS
No material regression. 4 KiB..16 MiB cells are within +-5% in the matrix, and re-measured with 8 interleaved rounds (`small_ab*.jsonl`): 1 MiB c4 77.7 vs 80.5 (GOMAXPROCS=1: 61.7 vs 61.7), 4 KiB c8 3.2 vs 3.3, 64 KiB c8 34.7 vs 36.4, then **order-swapped** (`small_ab_swapped`): 34.0 vs 33.8, 3.2 vs 3.2. The earlier 3-5% leans were run-order bias (the second variant in a pair is slightly slower), not code: below 32 MiB the path is the serial one with only the faster CDC scan. The matrix's single/dual-core 16 MiB cells (0.88-0.94x) were order bias too: re-run order-swapped (`small_ab_gomaxprocs_swapped`, 6 rounds) 16 MiB c1 is 62.3 vs 61.6 / 67.7 vs 68.0 / 71.7 vs 68.5 MiB/s (final vs base at GOMAXPROCS 1/2/4); large objects improve at every core count. Peak RSS: small unchanged (17-20 MiB); 64 MiB 34, 1 GiB 44 MiB, 4 concurrent streams 60 MiB (cap: 2 pipelines).

## 9. Failed or rejected approaches
- Pipeline for every object >= 4 MiB (first candidate): +13% CPU and -5..-10% on 16 MiB chunked at c4, +20 MiB RSS, no gain on the loose path (syscall/fsync bound, P1B). Fixed: 32 MiB floor, direct-pack sizes always, <= GOMAXPROCS/2 concurrent pipelines.
- 3-buffer pack writer: producer waited 0.8 s per GiB for a free buffer (wake-up latency of two consumers at 3 handoffs/ms); 8 buffers cut it to 0.2 s.
- 1 MiB batches x 8 buffers: peak RSS 56 MiB for the same throughput; trimmed to 0.75 MiB x 6 (41-44 MiB).
- Replacing the read-back SHA with a memcmp against the writer's buffer (0.1 ms/MiB): **rejected**, it would not detect a buffer modified between digest and write, which the digest check does.
- Verification before the fsync, or dropping read-back: rejected (changes what is verified, and when). `sync_file_range`: not portable in a single stdlib file; background `fsync` used instead.
- Fused single pass: this *is* the original design (every digest sees each chunk while L1/L2-resident); fusing cannot reduce instruction counts. Compute-only comparison (256 MiB, discard sink, `BenchmarkP1A_ComputeDesigns`): original fused serial pass **357 MB/s on any core count** (CPU-bound on one core, 2.8 CPU-s/GiB of pure compute); bounded pipeline 347 / 610 / **801 MB/s** at 1 / 2 / 4 CPUs (2.25x; the MD5 stage is its ceiling). Hence the pipeline is not started on one CPU, where it only adds hand-off cost.

## 10. Remaining bottleneck
Single stream: no stage is saturated; the VM runs at about 3 of 4 cores including the load generator, so what is left is CPU per byte: MD5 1.3 + SHA-256 4 passes 2.5 + file write 1.0 + CDC 0.6 CPU-s/GiB. The request goroutine is about 100% busy (read 0.4, CDC 0.6, wait for chunk digests 0.2, copy+hook 0.2, buffer wait 0.2 s/GiB at 2.1 s); a separate read+CDC goroutine could add roughly 10-15% only if more cores were free. 4 streams: CPU-bound (3.0 of 4 cores).

## 11. Headroom (evidence-based)
- Dropping the last redundant pass is done; further passes are required outputs. MD5 cannot be parallelised within a stream (about 650 MiB/s per core is the single-stream ceiling).
- Estimated ceiling on this VM with an idle client and perfect overlap: 6.0 CPU-s/GiB / 4 cores = 1.5 s/GiB, about 650 MiB/s, i.e. at most about 1.3x over today for one stream, 0 for saturated multi-stream. Larger gains now need fewer syscalls/copies (page-cache write 1.0 CPU-s/GiB) or different formats, not more compute scheduling.
- Unmeasured: slow-fsync devices (early fsync overlaps with ingest and removed flushes here, but this VM's fsync is cheap), sync/replicate, 5 TiB objects.

## 12. P1B implications
P1B (group commit, `casPubMu`, small-object overhead) is untouched; the loose path (<64 MiB, multipart parts, tail chunks) still costs 14-25 CPU-s/GiB and shows 1.0-1.2x here. `casStat` per chunk (about 1.4% of CPU), per-chunk loose-file creation and multipart completion's serial `casRead`+SHA are the remaining compute-adjacent costs; multipart should get direct-pack parts (PW-4) so completion can reuse this pipeline. Hooks now fire on background goroutines; P1B tests should use atomic counters (one existing test needed it).

## 13. Validation and status
Commits (4 production, plus tests/evidence) contain no AI attribution trailers; PR #104's footer was removed first (verified by re-reading the posted body).
| gate | result |
|---|---|
| build, `go vet`, gofmt, modules/deps (`go.mod` and `deps-proof` unchanged, stdlib only) | PASS |
| `validate.sh normal`: format, modules, **full root tests (363 s)**, s3 (137 s), sync (66 s), reproducible build | PASS |
| `validate.sh static` | **FAIL, pre-existing**: identical on clean `origin/main` (source map 34 of 35 entries, test map 23 of 40 entries); not a P1A regression, not repaired here |
| **`go test -race ./...` (full suite, 909 s)** and the pack/compression crash matrix (race, 79 s) | PASS |
| direct-pack, direct-pack-life (crash matrix, real-process kill), cas-batch, vectors, client (AWS SDK + minio-go), apps | PASS |
| new tests (`zeros3_p1a_test.go`, all also under `-race`): CDC equivalence, pipelined == serial (12 sizes x 4 readers x md5 on/off) with independent digests, read error / premature EOF / mid-chunk cancel / sink failure / pack-publish failure / corruption caught by background verify / crash in a background job / repeat chunks across in-flight packs / 8 concurrent PUTs + restart + GET / pack writer vs reference / sticky early-fsync error / 2,768-bit mutation equivalence of the two verifiers | PASS |
| defaults (`directPackMinBytes`, `directPackTarget`, compression off) and every format (pack v1, manifest, journal, store format floor) | unchanged. Same input through base and final gives **byte-identical packs** (equal file names = body SHA-256) and `FORMAT.json`; the **baseline binary `verify -deep`s and reads a store written by the new binary** (3 objs, 6 packs, 3,705 chunks, OK) |
| final-commit numbers | measured with a binary built from `zeros3.go` SHA-256 `35344677...833954ebd`, the file in the PR (re-checked) |
One existing test (`TestDirectPackLife_CrashMatrixConverges`) used a non-atomic hook counter and raced once hooks could fire on two pack jobs; made atomic with the same line count (test-only).
Ablation switch `ZEROS3_INGEST_ABLATE` (benchmark-only, unset in production) is kept so the measurements reproduce.
PR_STATUS_PLACEHOLDER

## 14. Final verdict
VERDICT_PLACEHOLDER
