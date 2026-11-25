ZeroS3 Post-C9 Frontier Queue v2.0: C10–C19
Master Codex execution prompt: one sequential, resumable research program, with an optional frontier reserve.
Repository: https://github.com/insightlabs38-pixel/zeros3
Development branch: research/next
Required completed ancestor: 3cf73e37205c98525686fbf470756dc4a2e46c66 (C9_SPECIALIZED_VALUE)
Stable main reference: 2ac9caba60cc050a595cefef7b791aa718803c0e at last verification. Verify it, never modify it.
Historical comparisons: B10 dcefbcb, C1 fe14dfd, C2 4bc66f3, C3–C9 as recorded in testing-harnesses/competitive/.
Recommended agent: Codex with High reasoning effort. One sequential agent; no automatic subagents, Codex delegation, or multiple agents unless the user explicitly authorizes it.
Objective: push ZeroS3 substantially beyond its present archive-specialized frontier through exact compressed reads, foreground-service control, fundamental lossless compression, cross-object economics, general-purpose data workloads, incremental indexing, streaming, transfer, and robust storage research.
0. Mission and strategic posture
C3–C9 are finished. Do not reopen their original queues or rerun their entire campaigns. Read their final reports and use the implemented mechanisms. Their final head is the mandatory starting ancestor, not a request to reset newer valid work.

The previous program demonstrated a valuable but narrower result than the long-term ZeroS3 ambition:
- C3/C8/C9: a receiver with a previously verified canonical authority base can import a TypeScript revision using 350,034 HTTP bytes, versus about 8.38 MB for peer CDC. In the paced C9 comparison, scoped native transfer took 1.51 s, compared with 5.52 s for ordinary S3 PUT and 11.04 s for peer CDC. The result depends on receiver knowledge, preparation cost, and link characteristics.
- C4: 49,152 signed S3 operations showed no consistent default S3 throughput regression. Linux ARM64 cross-compilation and QEMU operational smoke passed; native ARM64 hardware performance and physical power-loss qualification remain open.
- C5/C9: real TypeScript archives retained about 58% less storage and SST histories about 63% less after proper finalization. Archived TypeScript full-object reads nevertheless took about 1.1–1.2 s, versus ~20 ms on ordinary CDC in a C5 lane. MobileNetV2 checkpoints and high-entropy controls had little additional archive gain.
- C6: keeping byte-identical recompressed packs avoided about 41 MB of unnecessary pack staging writes on selected workloads. Alternative reconstruction-order packing and restartable dictionary placement did not improve the combined storage/read frontier.
- C7: a verified posting index enabled bounded new-revision optimization, reducing dense-history CPU from approximately 1.49 s to 0.31 s with near-matched retained data. The 1,031-content index occupied 495,616 allocated bytes. Full-store analysis and metadata economics remain relevant.
- C8: single-pass native import reduced receiver CPU 4.98 → 1.84 s and eliminated relay file staging on eligible small closures. Source planning still generated substantial scratch I/O, and foreground p99 rose under concurrent import and online maintenance.
- C9: the artifact mirror, Parquet/SST consumer restores, and Restic snapshots passed exact end-to-end verification. This proves those use cases work; it does not constrain the long-term architectural research to artifact archival.
Do not pigeonhole ZeroS3 as an artifact-only product. It is pre-1.0, and full production qualification will occur later if resources permit. The research branch should explore the broad frontier of high-performance, lossless, content-aware object storage: ordinary S3 workloads, versioned datasets, databases, ML checkpoints, VM images, logs, compressed software, snapshots, backup, and efficient transport. Technical evidence should decide which domains eventually deserve first-release support; do not infer product scope from one small benchmark campaign.

Equally, do not confuse ambition with indiscriminate implementation. An integrated, genuinely novel primitive with measured gains is more valuable than ten new format recognizers, a pile of redundant packages, or a report that overstates scope. Serious experimental failures are acceptable; wrong bytes, invented evidence, and bypassed correctness tests are not.
Global priorities
1. Make the storage-efficient forms of popular compressed objects fast to retrieve exactly.
2. Keep ordinary foreground S3 operations responsive while imports, replay, maintenance, and indexing run.
3. Push general-purpose lossless compression and differential coding, not just further archive wrapper support.
4. Investigate broader reusable data structures on non-artifact histories, especially stateful large files and model/data representations.
5. Reduce historical-analysis, physical-layout, memory, and write-amplification costs.
6. Preserve research freedom while establishing honest cross-domain Pareto frontiers and robust correctness.
1. Autonomous overnight and resume contract
This queue is one chronological, sequential campaign. Execute C10, C11, ... C19, with evidence-based changes of order permitted only when a concrete dependency or superior high-value experiment justifies them. Work through successive batches without asking for permission after each checkpoint; the user authorized this queue. Stop on an explicit user stop, a security/credential ambiguity, or a task that materially exceeds authorized resources. Do not start unscheduled external jobs or billable services.

At every new or resumed agent session:
1. Fetch GitHub refs and inspect the current research/next head, ancestry, main, working tree, last commits, and any unfinished checkpoint. Preserve legitimate commits beyond the starting SHA. Do not reset or force push.
2. Identify completed C10–C19 reports under testing-harnesses/competitive/cNN/README.md, and inspect the last checkpoint's raw data, runner, and tests. A completed batch with a credible report and passing focused acceptance must not be redone merely because this is a new workspace.
3. Read the relevant actual code in zeros3.go, current tests, source map generator, previous C3–C9 evidence, and the applicable historical B1–B10 or Z3/Z4 research. Do not implement a feature that already exists under a different name.
4. Inspect current CPU quota, RAM, filesystem, available space, page-cache controls, network restrictions, and compiler/tool versions. Prior local scratch is not guaranteed to survive.
5. Rehydrate only the minimal verified corpus and tooling required for the next incomplete experiment. Prefer immutable public source hashes and reproducible producers over retained large datasets.
6. Continue from the first incomplete meaningful checkpoint, not the first section of this file. Preserve important failed experiments when they inform later choices.
7. After a coherent implemented/tested checkpoint, commit and push to research/next. Prefer about 2–5 substantive commits per batch, with extra commits only for independent changes. No PR per microtask.
8. If interrupted, finish by pushing a small factual checkpoint status identifying verified work, incomplete work, reproducible commands, and the exact next action. Do not claim to be working asynchronously after the session ends.
Git author and committer: insightlabs38-pixel <insightlabs38@gmail.com>.

Do not include Co-authored-by, model/tool attribution, generated-by footers, automation/session links, or other signatures in commits or PR text. Inspect resulting commit metadata. Keep commit messages and any PR body under ten lines unless an unusually complex integration genuinely needs more. Do not modify main or merge research to main automatically.

Writing/report style: short, coherent, technically specific. Each batch's README.md has a hard limit of 90 physical lines, ideally 60–75. Use wc -l. Do not evade this by writing a 300-line explanatory Markdown appendix; preregistration and design notes may be somewhat longer when needed, but raw details belong in JSONL/CSV, executable scripts, and generated tables. Keep the completed-batch chat reply about 20–35 substantive lines. No verbose agent diary, repetitive summaries, dramatic feature claims, or arbitrary commit chatter.

All implementation should remain on the sequential agent's current branch. If the execution environment disappears, Git commits and reproducibility manifests must be sufficient to continue.
2. Evidence, resource, and architecture requirements
2.1 What counts as a valid improvement
- Storage: physical filesystem allocation after comparable full finalization, with namespaces, indexes, records, recipes, dependency closures, dictionaries, and live pack slack counted. Show absolute bytes and ratios. Keep initial, intermediate, and peak allocation separate.
- Transfer: separate client-to-server bytes, relay/peer bytes, actual measured wire counters, source preparation and receiver reconstruction CPU, and post-transfer physical storage. Preserve receiver-knowledge assumptions and auth scope.
- Reads: distinguish first touch/process-cold, demonstrably kernel-cold when controlled, steady warm, cache hit, and repeated-neighbor; measure 4 KiB, 64 KiB, 1 MiB, and whole-object access plus concurrency and p50/p95/p99.
- CPU and I/O: elapsed versus CPU time, client versus server, temporary writes, pread/`pwrite`, fsync, memory, cache use, decode work, read amplification, and foreground interference.
- Correctness: exact original object bytes (SHA-256), format identities, metadata, history, S3 conditional operations, and recovery. Semantically equivalent recompressed output is not exact if source bytes differ.
- Controls: old binary at the actual pre-batch SHA, strongest applicable current incumbent and external compressor/system, a held-out corpus, and at least one negative/incompressible control. Isolate benchmark corrections from code gains.
Pre-register primary input families, configurations, limits, metrics, and gates before the final comparative campaign. Small pilot experiments and corrections may precede registration and must be labeled. Rotate or randomize measurement order, repeat significant comparisons, and preserve original negative rows. Avoid ranking noisy four-client trials using a single median.
2.2 Resource discipline
Use bounded actual scratch, sequential backend runs, and a generous free-space margin. Most fixtures should be about 1–512 MiB, with controlled small larger cases if the measured environment permits. Synthetic million-key metadata-only tests are allowed; never require a physical million-object store with large payloads, multi-terabyte disks, or expensive cloud infrastructure. No full T1/36-stage release qualification unless separately requested. Never assume persistent workspaces.

External compressors/encoders, xdelta3/bsdiff, zstd, zchunk, research Python/Rust/C++, benchmark services, and other tools may be used as experimental baselines. Production remains Go standard-library-only, no CGO requirement, and ordinarily a single zeros3.go, unless an explicit user-approved architecture change occurs. An algorithm tested using an external library is not thereby production-integrated. Check licenses before copying third-party algorithms/code into production; prefer independent implementations of documented algorithms or compatible existing project code.

Optional Hugging Face testing uses only previously authorized disposable resources and injected secrets. No token printing or persistent storage. Do not upload private user data to public resources. Enforce byte caps; do not create paid infrastructure, other repositories, or buckets. Hosted transfer results are not local physical CAS accounting.
2.3 Research selection and stopping rules
Each batch must attempt at least one substantial technical approach matched to a real bottleneck, not just run benchmarks and write a report. For each serious candidate, record PROMOTE, SPECIALIZED, REDESIGN, RESEARCH_ONLY, or KILL, with measured evidence and reasons. A first difficult prototype may merit a focused redesign; a repeatedly negative cost model does not deserve an entire subsequent reset. Avoid abandoning technically promising ideas merely because implementation is complex, but do not require an arbitrary format count or LOC count to declare progress.

Use the existing ZeroS3 graph, optimizer, authority, record-group, CDC/CAS, journal, snapshot, transfer, recovery, and namespace primitives where they are sound. Avoid parallel duplicate engines. New persistent formats must be explicitly versioned/feature-gated, safe against older binaries, recoverable after crashes, and included in GC, deep verify, migration, transfer, revert, and repair as applicable.
2.4 Preserve external behavior
- Default S3 requests remain fast, complete, and compatible; high-cost research mechanisms stay opt-in unless convincing multi-workload evidence justifies changing defaults.
- Preserve ETag semantics, SigV4, metadata, conditional requests, listing, version/history and snapshots, object visibility, and durability boundaries.
- No unauthenticated disclosure of content fingerprints, similarity evidence, dictionary identity, or cross-tenant data through new protocols.
- Integrity and fault refusal take priority over saving storage or completing a benchmark. No wrong-byte fallback, no unchecked cache, no silent dependency drop.
- Do not enlarge existing production/maturity claims because an experimental benchmark passed; main remains unchanged.
C10: Exact compressed full-read and range-reconstruction frontier
Priority: Immediate. Central problem: C9 TypeScript archive full reads ≈1.2 s, while ordinary CDC reads were in the tens of milliseconds. The archive's storage advantage is economically difficult to realize in frequent-access applications unless its reconstruction path changes substantially.
C10-A. Determine the exact source of the 1.2 s
Rebuild the C9 TypeScript mirror archive and relevant B8/B10 RubyGems, gzip/ZIP, Parquet, and SST controls. Profile first full read, second full read, 4 KiB ranges, 64 KiB ranges and overlapping reads. Attribute wall/CPU/I/O to canonical/deep-stream delta base retrieval, wrapper exact replay, decoder startup, recipe parsing, restart checkpoints, verification, pread, allocations, and repeated traversal. Capture flame graphs or compact CPU profiles and actual bytes decoded and emitted. Do not assume every archive pays the same penalty; an SST block is not a gzip wrapper.
C10-B. Attack the full-read algorithm, not just the cache
Investigate at least two distinct mechanisms from a reasoned shortlist:
- Direct sequential replay with one dependency traversal and one verified emission path, avoiding nested per-segment range recursion, redundant hashing, and repeat decoder initialization.
- Generation and evaluation of exact replay checkpoints: compressor/decoder state, DEFLATE bit alignment, sliding windows, entropy tables, or reproducible exact-encoder state where valid. Checkpoints must allow exact original-byte generation, not only decoded plaintext access.
- Bit-exact compressed output slicing, independently verified compressed subranges, and recipe residuals that let portions be served without replaying every preceding output byte.
- Adaptive sparse checkpoints/materialization, possibly guided by object size, restart cost, and access characteristics, with a clearly priced index and without storing a full duplicate object by default.
- Shared verified intermediate streams across several wrapper objects and controlled sequential prefetch on whole reads.
- Faster exact gzip/zstd/ZIP replay primitives and efficient bounded memory reuse, without external runtime codec dependencies.
Inspect existing B1/B2, B5, B8/B9/B10, C2, and C8 work first. The purpose is to improve the actual production reconstruction path rather than reimplement known random-access infrastructure.
C10-C. Storage and latency Pareto frontier
For every viable approach, measure end-to-end bytes retained, metadata/checkpoint overhead, cold and warm latency, whole/read concurrency, CPU, RSS, decoder work, and proof costs. Compare the same frozen artifacts under CDC, canonical baseline, deep-stream baseline, C10, and any optional caches. Include compressible and incompressible controls. At least one independent gzip-heavy artifact family must be held out from tuning.

A target worth pursuing is several-fold faster exact full reads on the archive family without giving back most of the retained-byte advantage. Treat this as a research aim, not a preordained pass claim. Evaluate big jumps before accepting small micro-optimizations. Large requests may use different execution plans from sparse reads.
C10-D. End state
Integrate only mechanisms supported by exact corpus replays and deterministic verification. Test compressed output alignment, truncated recipes, damaged checkpoints, base removal, shared children, concurrent readers, reopen, and fallback. Give the read-cost optimizer accurate revised costs. Leave expensive materialization opt-in. Report storage/read/CPU tradeoffs and the strongest unresolved compression bottleneck. Place scripts and data under testing-harnesses/competitive/c10/.
C11: Foreground latency isolation, maintenance QoS, and concurrency architecture
Priority: Immediate after C10. Central problem: C8 import and online maintenance increased foreground p99 on a two-core node despite large reconstruction gains. The largest opportunities may be scheduling and isolation rather than faster codecs alone.
C11-A. Reproduce the interference on real concurrent traffic
Create controlled workload mixes: 1/4/8/16 S3 clients with a concurrent scoped import, GC, repack, archive finalization, indexed analysis, and restore. Include small object PUT/GET/range/HEAD/LIST and mixed contention; record CPU quota, cgroup throttling, Go scheduler profiles, GC pauses, mutex/block profiles, fsync bursts, disk queue, allocations, tail latencies, background progress, and recovery. Use the C8/C9 source and receiver preparation as matched workloads. Separate root-lock contention from competing CPU and storage bandwidth.
C11-B. Build a bounded, cooperative resource-control mechanism
Explore a compact work-class admission / budgeted cooperative scheduler across maintenance, optimization, import, and foreground I/O. Candidate levers include: CPU concurrency caps for expensive decoders, token/byte budgets for disk reads/writes and fsync, bounded request reservations, pause/yield points during long compactions, queryable background progress, priority inheritance or safe lock sequencing, and per-pass work quotas. Preserve work-conserving behavior and avoid an unnecessary generic task scheduler.

Examine whether read-only readers can continue against a verified immutable generation while a new generation is compacted, without extending stale-data windows or introducing unsafe deletion. Use existing flocks, reader leases, journal visibility and scoped reclamation. No bypass of durable acknowledgments.
C11-C. Quantify QoS, not merely reduced p99
Compare background-off, C10/C9 baseline, naive throttling, and selected QoS mechanism at identical logical load. Measure foreground p50/p95/p99, S3 throughput, background throughput, total completion time, CPU/RSS, I/O amplification, fairness, starvation risk, and time to reclaim. Include a 2-CPU small host and one different resource profile when practical. Ensure critical maintenance eventually completes under sustained traffic, or state a documented bounded suspension policy.
C11-D. Recovery and acceptance
Test cancellation, restart, SIGKILL at new durable boundaries (if any), busy-reader leases, queued background jobs, import retries, shutdown while throttled, and fixed budgets under resource exhaustion. Avoid large unbounded waiting queues. Preserve default no-maintenance S3 throughput and compatibility. Keep new policies opt-in until matched tail results justify wider use. Report under testing-harnesses/competitive/c11/.
C12: Format-neutral lossless delta and similarity-coding frontier
Priority: High. Thesis: A great deal of ZeroS3 development has improved which related objects it discovers and how it stores compressed containers. There may still be major gains in the core differential coding algorithm itself on arbitrary byte streams, including inputs for which specialized replay is irrelevant.
C12-A. Establish modern, independent delta baselines
Audit current CDC, raw deltas, windowed ZW variants, B6 multi-base candidates, B7 discovery, B8 canonical, C7 indexing and the exact structure of the stored delta program. Compare under matched full physical storage against at least several relevant research baselines (such as xdelta3/VCDIFF, bsdiff-like suffix-array strategies, zstd --patch-from where available, external long-distance matching, and a simple CDC+compression implementation). Treat external libraries as laboratory references, not production dependencies. Verify matched corpus and reconstruction; compare compressed delta size, encoding CPU/RSS, random-access cost and end-to-end store layout.
C12-B. Explore genuinely different algorithms
Pre-register two or more serious methods based on observed failure categories:
- Multi-base factorization with globally selected source intervals, efficient relative addressing, and a constrained dependency graph rather than independent object-pair deltas.
- Content-defined sub-chunk or variable-granularity matching for shifted data, insertions, embedded duplicates, and mixed compressibility; adaptive boundary placement and edit stability.
- Hybrid suffix-array/hash-chain/rolling fingerprint search that allocates CPU only to hard unmatched regions; exact and bounded memory.
- Delta-program entropy coding, table factoring, literal-run compression, address predictors, or history-specific codebooks priced at real allocation size.
- Reversible byte-plane/XOR/columnar transforms for generic fixed-record streams, with opaque binary and random controls.
- Chunk and delta representations selected jointly across several history revisions rather than a fixed previous-version chain.
Do not run a large Cartesian sweep over arbitrary parameter names. Pick a conceptual model, implement a small exact prototype, measure it, and redesign the best candidate if its bottleneck is identifiable.
C12-C. Multi-domain workload matrix
Use verified related source releases, binary revisions, large SQLite/VM-page-style files, event/log streams, tensor/checkpoint revisions, compressed assets, and low-overlap controls. Include an independent held-out history not previously used in the B-series. Distinguish original compressed artifacts from decompressed canonical streams. Compare near-duplicate, shifted, reordered, appended, and unrelated patterns. Report CPU per GiB of analyzed input and a clear storage/decode/P99 Pareto frontier.
C12-D. Integration gate
Promote a candidate only if it beats existing representations after all program metadata and component closure costs, or meaningfully improves CPU/read costs at comparable retained allocation. Ensure exact fresh-process decoding, random-range correctness, deep verify, crash-safe publication, GC/revert/transfer support, and no accidental worsening of the global optimizer incumbent. Research-only high-compression but unacceptably expensive variants may remain in isolated test code with an honest negative finding. Work under testing-harnesses/competitive/c12/.
C13: Shared dictionaries, global entropy, and small-object physical compression
Priority: High, after C12 gives meaningful byte-level economics. Thesis: Independent object/record compression leaves statistical redundancy across related small records and mutable histories. A shared, verified, bounded compression context might outperform both per-record DEFLATE and C6's unsuccessful simple pack reordering.
C13-A. Audit C6's negative result and establish the true floor
Read C6's placement and restartable dictionary results. Its reconstruction-order packing and dictionary blocks increased CPU and storage; never present the same experiment under a new name. Profile a common mixture of small JSON/log objects, repeated configs, software source fragments, database pages, model metadata, sparse VM regions, and unrelated controls. Attribute bytes to block overhead, entropy cost, codebook metadata, inode floor, shared-record indexes and unchanged contents. Establish the best current B9/C6 grouped record and fully finalized pack baseline.
C13-B. Research cross-object compression with exact ownership
Explore at least two of these with independent baselines:
- Cluster-shared dictionaries with verified dictionary digests and explicit reference counts, trained from already authorized retained data. Compare raw prefix dictionaries versus trained dictionaries and stratified samples.
- Cohort/epoch pack compression of small compatible objects with sparse seek indexes, bounded block independence, and copy-on-write aging, rather than indiscriminate solid compression.
- Joint literal/delta entropy coding, shared phrase tables, immutable compression contexts, and per-object residual codes. Compare the physical cost of dictionary materialization and all references.
- Sliding-window or long-distance matching across a bounded cluster, indexed restart points, or select block sizes based on read-size distributions.
- Data-driven composition: use C12 deltas before entropy coding, or entropy-aware packing before delta, and establish which order actually wins.
Use zchunk/zstd seekable/dictionary design as research references, not authority to import new production dependencies. Useful literature/implementations to inspect: https://github.com/zchunk/zchunk, https://github.com/meta/zstd/tree/dev/contrib/seekable_format, https://www.rfc-editor.org/rfc/rfc8878, and https://jmacd.github.io/xdelta/.
C13-C. Critical economics and fault boundaries
Measure true allocated bytes including dictionary and index storage, compression CPU, training CPU, block write amplification, cold 4 KiB reads, whole reads, 8-client throughput, RSS and effects of object deletion. Verify recovery when a dictionary is absent/corrupt, when a shared dictionary has several parents, during online compaction, after namespace changes, and under mixed old/new formats. Dictionaries must never be shared through an unauthorized cross-scope identity leak.
C13-D. Outcome gate
Keep only candidates that clearly improve the storage/read/CPU frontier beyond C6, not simple layout rearrangements. A storage-only archival dictionary with slow sparse reads may remain opt-in if the retained gain and migration cost justify it. If ordinary entropy sources or high-entropy controls offer no savings, say so. Report under testing-harnesses/competitive/c13/.
C14: Beyond archives, general-purpose data and checkpoint compression
Priority: High. Thesis: MobileNetV2 and opaque checkpoints were correctly negative in C5. That result does not rule out exact compression in other model revisions, databases, virtual disks, event streams or mutable datasets. Explore broader sources without assuming they must compress well.
C14-A. Independent workload discovery
Prepare a verified, disk-bounded panel with at least four categories and select two for deep implementation based on measured headroom:
1. ML checkpoint histories: FP32/FP16/BF16 and quantized tensors, repeated pretrained layers, sparse fine-tunes, optimizer-state changes, reordered files, adapter weights and metadata changes. Include truly independent weights and random controls.
2. Databases: SQLite page histories and WAL/checkpoint transitions, Parquet with dictionary renumbering, RocksDB compaction/SST rebuilds, and a schema-evolution control. Preserve exact file bytes and application-level verification.
3. Virtual machine/volume images: sparse raw disk images, repeated filesystem blocks, shifted partitions, near-duplicate snapshots and a compressed-image control. Prefer modest generated images with independent exact filesystem verification.
4. Operational/event data: append-heavy logs, columnar event fragments, JSON telemetry, binary protobuf-like records and daily partitioned revisions, with irregular variable-length records and reordering.
5. Media and binaries: compiled executable families, image/media revisions, compressed installers and already highly compressed files; measure when the lossless upper bound is poor.
Use public redistributable artifacts where possible. Synthetic controlled generators are acceptable for isolating a mechanism but cannot replace all real independent tests. Do not commit huge source data.
C14-B. Algorithms worth testing
Use both format-neutral C12 methods and appropriate reversible semantic transforms. Examples: bitwise XOR residuals or bitplane restructuring for exact tensor dtypes; block-relative offsets and content-stable logical page identity for databases; sparse-run maps with CDC for VM images; record-column factoring for logs; reversible dictionary-index and RLE normalization for Parquet. Preserve exact original file bytes, including headers, padding, checksums, row/page order, float bit patterns, quantization bytes and source codec layout. No lossy dedup, no approximate numerical thresholds, no semantic-only table equality.
C14-C. Avoid niche traps
At least one selected pathway should be useful without knowing that the input is a tarball/ZIP/gem. A format-specific transform must justify complexity through bytes, CPU and access performance. Do not implement all named formats. If model weights have no exact overlap, record the limit and pivot to more promising classes rather than hallucinating compressibility. Differentiate generic exact similarity from format-specific replay and from simple entropy compression.
C14-D. Production integration and comparisons
For promoted pathways, verify complete content versions, range reads, random corruption, cold reopen, post-GC exact retrieval, and application-level restoration (e.g. SQLite query equivalence and byte-for-byte file equality, tensor exact SHA and dtype). Compare with Restic/Kopia or xdelta/zstd when relevant and an ordinary ZeroS3 CDC baseline. Report retained bytes, update cost, CPU/RSS, physical writes and read/restore latency under testing-harnesses/competitive/c14/.
C15: Incremental content graphs, similarity indexes, and scalable namespace economics
Priority: High, after C12–C14 establish which candidate families are useful. Thesis: C7 showed that indexed new-revision optimization can recover almost the full benefit in one pass, but a 495,616-byte index for 1,031 contents and rescanning work under churn could erase the wins on less favorable populations.
C15-A. Quantify graph/index overhead and freshness costs
On real C7 workloads and controlled metadata-only scale, measure index size per retained object, query CPU, append and tombstone costs, checkpoint open time, journal replay, index verification, memory, and bytes rewritten. Track the existing B3 namespace-v4 checkpoint and incremental C7 postings. Distinguish a quick read-only metadata scalability experiment from a real S3 object-storage scale claim.
C15-B. Investigate smarter incremental discovery
Explore multiple credible alternatives, implementing those justified by C12–C14:
- Hierarchical or partitioned postings with compact content signatures, adaptive sparse/dense sketch selection, and well-defined false negatives/positives. Candidate hints must always be checked against exact verified bytes.
- Cohort-specific similarity indexes that avoid loading irrelevant formats, sizes or histories, while preserving cross-key and cross-format opportunities under an authorized scope.
- Incrementally maintained neighborhood / dependence groups for canonical and shared dictionary candidates; avoid full graph reevaluation after an unrelated object arrives.
- Update-aware multi-tier index (mutable small delta + immutable compacted runs), bounded write amplification, and pause/resume compaction.
- Cached candidate analyses with content/format digests, producer-version policy keys, exact invalidation, and an explicit expiration/resource bound.
- Efficient identification of marginal shared benefits from new/removed revisions, not just nearest-neighbor base lookup.
Avoid an approximate sketch that silently misses practically all hard compressed-container matches. Assess recall, precision, verification CPU, physical index bytes and search quality together.
C15-C. Correctness under namespace churn
Exercise uploads, overwrites, copies, pruning, restore/revert, imports, checkpoint snapshots, crashes during index publication, tombstones, compaction and index rebuild after corruption. A missing/stale index must degrade safely to verified fallback, never misidentify a base, lose objects, or leak information across buckets/authorization scopes. Preserve old-reader behavior and no-op analysis reuse. Include bounds on journal generation size, retained index generations and memory.
C15-D. Benchmark acceptance
Compare C7 and C15 on one new revision amid 64, 1,024 and larger metadata-only populations; 20–100 revision edits, a hot target mixed into unrelated files, and a hard cross-format history. Report index bytes including allocation, update CPU, query CPU, recall, candidate quality, final bytes and foreground request interference. Promote if lower total storage-plus-index or analysis cost at equivalent quality. Otherwise keep only targeted improvements. Place output in testing-harnesses/competitive/c15/.
C16: Native transfer with weak receiver knowledge, WAN economics, and broader compatibility
Priority: Follow compression and index improvements. Thesis: C3's 350 KB transfer is valuable but required a receiver already holding a verified canonical authority component. The more general problem is efficient, privacy-safe transfer when neither endpoint has precomputed the ideal representation, under real network delay and finite CPU.
C16-A. Establish the known-knowledge frontier
Reproduce the C3/C8/C9 positive TypeScript lane and negative controls in three receiver states: complete prior authority, same logical prior object stored as ordinary CDC, and no related data. Add a destination with related data under a different authorized key but no explicit prior base. Include compressed revisions, database/model data from C14, shifted binary and entropy. Attribute source preparation, discovery, index construction, wire bytes (both legs), receiver reconstruction, network latency, memory and post-transfer storage. Compare native CDC, scoped authority closure, ordinary PUT, and local OpenXet or Hugging Face Xet where measurable.
C16-B. Extend discovery only where economically justified
Research techniques for useful cross-version reconstruction without a pre-installed canonical authority body:
- On-demand transient verified base extraction of an existing ordinary CDC object at the receiver, bounded by CPU, memory and scratch, with exact content identity.
- Authorization-scoped, privacy-preserving content hints and bounded hierarchical sketches that can discover reusable structure without disclosing an unrelated repository's contents.
- Recursive reconciliation of missing canonical/delta dependencies, with explicit request and byte budgets, deduplicated scheduling and safe fallback to CDC/PUT.
- Compatible incremental capability negotiation and old-peer fallbacks, avoiding an all-or-nothing protocol upgrade.
- Choosing source-side versus receiver-side work based on CPU price, RTT, bandwidth, prepared bases and expected reuse, with honest cost modeling.
- Avoiding a costly full archive optimizer pass solely to save one small network upload.
Do not invent a generic server-to-server distributed consensus protocol, grant peers unauthorized access, or trust unverified remote content hashes. Use signed scopes, bounded advertisements, checked manifests, and exact SHA-256 authority.
C16-C. Implement and test economically plausible methods
Pick mechanisms that survive small reproducible pilots. Test target crash at component flush, manifest publication and journal sync if publication semantics change; duplicate retries, damaged base claims, unsupported capabilities, clock/replay controls, scope denial and mixed old/new peers. Preserve ordinary S3 behavior and a safe generic CDC fallback.
C16-D. Transfer break-even curves
Test unlimited loopback and shaped RTT/bandwidth combinations representative of fast LAN, ordinary WAN, and bandwidth-constrained links. Time source prep separately, also amortized across several transfers; report client and server CPU, both-leg wire bytes, post-transfer storage, and exact throughput. If authorized and cost-bounded, an additional Hugging Face Xet lane may provide real hosted transfer context, not same-hardware backend latency or physical CAS size. State where native transfer actually wins, loses, or cannot be prepared cheaply. Report in testing-harnesses/competitive/c16/.
C17: Streaming storage pipeline and I/O amplification frontier
Priority: High after read and transfer path investigations. Thesis: C8 eliminated one native-import reconstruction pass, but source planning still emitted many pread/`fsync` calls and large seekable scratch streams. ZeroS3 should minimize repeated physical I/O from initial content through canonical transform, candidate analysis, publication, and finalization.
C17-A. Trace actual dataflow and repeated work
Instrument at least one compressed source history, a general-purpose tensor/DB history and an unrelated control. Break down each input byte's path: S3 ingest, CDC, candidate discovery, decoded stream generation, scratch writes, delta encoding, closure verification, publishing, GC, repack, reader execution, and native transfer. Compare the C8 traced TypeScript planner's approximately 67.83 MB scratch and 779 fsync calls, checking environmental variance. Identify redundant reads, copies, fsync operations, staging and reconstruct/deconstruct cycles.
C17-B. Design a bounded fused execution path
Research and implement one or more of:
- Streamed decode → canonical hash → delta candidate construction → verified component publication using bounded buffers and an exact end-to-end proof, without writing the same decoded stream multiple times.
- Seekable spool replacement with safe bounded-memory windows, compressed independent scratch blocks, deterministic indexed restart positions, or an external-memory algorithm that supports the base reads actually required by the optimizer.
- An explicit execution DAG for reusable dependency requests, coalesced I/O, shared hash computations and pipelined whole-read output.
- Buffer pooling and opportunistic zero-copy interfaces where portable Go semantics permit, without unsafe unchecked output or unbounded memory retention.
- Transaction-safe staged writes that batch work while preserving durable namespace acknowledgment and independent component verification.
- Streaming large native closures with controlled memory, backpressure, retries and recovery, expanding beyond C8's ≤1 MiB in-memory relay benefit.
Do not blindly eliminate every seekable temporary file. Some algorithms require random source access; prove any replacement preserves candidate quality and range/read correctness. A compressed spool that saves disk but increases 4 KiB latency by two orders of magnitude is not automatically an improvement.
C17-C. Measure performance in a resource matrix
Include a two-core, low-memory case and a more parallel case where available. Measure physical bytes read/written, syscall counts, fsyncs, peak scratch, CPU, wall, GC pauses, foreground tail latency, resulting retained storage and candidate quality. Compare against C9/C16 and the best independently configured predecessors. Ensure the source preparation savings do not simply move cost into the receiver or increase durable I/O elsewhere.
C17-D. Safety and output
Test short reads, partial writes, cancellation, truncated streams, compression bombs, invalid units, repeated base reads, SIGKILL around newly changed durable transitions, retry convergence, and pack/namespace lock ownership. Keep fallback seekable execution for unsupported cases. Report under testing-harnesses/competitive/c17/.
C18: Failure domains, recovery economics, portability, and broader S3 consumers
Priority: Before claiming broad usefulness. Thesis: Storage innovation is not deployment robustness. Explore architectural barriers to broader use without turning this research phase into an unfunded production qualification campaign.
C18-A. Define independently meaningful failure domains
Audit acknowledged writes, namespace/journal checkpoints, grouped records, canonical/deep child dependencies, shared dictionaries from C13, indexed generations from C15, and transfer closures from C16. Build a bounded failure-mode map: power loss before/after filesystem flush, interrupted compaction, missing pack, corrupt group, missing canonical base, stale reader, damaged checkpoint, partial transfer, and metadata reconstruction. Distinguish what prior SIGKILL tests establish from actual physical power-loss behavior, which remains unqualified.
C18-B. Prototype the most consequential recovery improvements
Pick high-value, concrete changes where evidence supports them:
- Incremental or targeted verified repair of missing dependency subgraphs, rather than expensive full-object or whole-store restore.
- Recovery-safe metadata and dictionary indexing without destructive long scans after crashes.
- Consistent immutable-generation views for concurrent readers and maintenance, with bounded stale-view handling and reclamation safety.
- Portable snapshot/restore economics for canonical/deep graphs, keeping transfer and restorable independent authority exact.
- Health/status surfaces that expose blocked GC, missing dependencies, background QoS state, temporary capacity and repair requirements without creating a heavy monitoring service.
- Minimal automated failure-injection harnesses (faulted filesystem operations, SIGKILL and controlled torn writes) targeting new durable boundaries; do not overclaim actual power-loss durability.
Avoid adding distributed consensus, cluster control planes, IAM/STS/KMS, or erasure coding solely because they seem production-like. Consider those as research-only spikes only if a measured use case and bounded prototype justify them. Single-node architecture may remain a rational choice for now.
C18-C. Platform and consumer breadth
Repeat focused static Linux/arm64 build and QEMU smoke, and use native ARM64 hardware only if available without new spending. Compare byte exactness, performance and filesystem behavior; never call QEMU timings native ARM64 benchmarks. Exercise representative official AWS SDK, Boto3, Restic and additional real consumers based on discovered gaps. Candidate integration workloads include ML artifact downloads, container registry push/pull, backup target, database snapshot fetch, build-cache reuse, or object-backed analytics. Prefer real operation traces, not a fake single-operation driver.
C18-D. Evidence limits
Distinguish research proof, recovery smoke, power-loss qualification, capacity/scaling qualification and production security. Continue using bounded actual fault cuts and disjoint root test partitions. Report exact restored contents, repair bytes, recovery CPU, tail latency, and operational costs in testing-harnesses/competitive/c18/. Do not change main or promise a production release.
C19: Independent cross-domain frontier and long-range architecture decision
Priority: Final integrated evaluation of the strongest C10–C18 work. Thesis: A useful research platform should emerge with a quantifiable advantage across multiple regimes, not merely a successful archive-only synthetic lane. C19 must force each major new technique to confront independent workloads, strong external references and total system economics.
C19-A. Freeze the integrated source and adversarial baselines
At the start of C19, freeze a Git SHA for the current research engine and the strongest unchanged predecessors (C9 and applicable per-batch predecessor). Rebuild pinned competitors from C1/C5 if still feasible: MinIO/RustFS for S3, Restic/Kopia/Borg for snapshot/dedup, OpenXet or official HF Xet for network economics, plus independent compression/delta tools for specific codecs. Reuse verified corpus manifests where valid; add at least two held-out real families not used during C10–C18 optimization. Rotate trials and report variance.
C19-B. Evaluate several distinct use-case frontiers
Construct separate honest scoreboards for:
1. General S3: immutable and mutable objects, small/medium/large reads, metadata operations, foreground latency under maintenance, ingest, restart and physical allocation.
2. Versioned compressed software: exact original archive bytes, whole/sparse read costs, retained history, migration CPU, native transfer and portable restore.
3. Data/ML: representative tensor/checkpoint revisions, SQLite/Parquet/SST and data files, native client read/restore integrity, realistic compression outcomes including negatives.
4. Large-state snapshots: VM/page-like or backup histories, changed/shifted/zero regions, incremental writes, pruning and independent full recovery.
5. Native/WAN transfer: prepared and unprepared receiver, identical/shifted/unrelated, latency/bandwidth response, all source/receiver costs and authorization constraints.
Do not aggregate incomparable interfaces into a single winner or hide durability/replication differences. Use a resource-adjusted Pareto view: retained bytes, elapsed/CPU ingest, optimization, read latency distribution, foreground p99, write amplification, network bytes, recovery cost, index/dictionary overhead and platform footprint.
C19-C. Architectural review without premature product narrowing
Produce an evidence-based long-range options analysis. Consider at least:
- A general-purpose single-node S3/CAS server with optional advanced content-native features.
- A differentiated version-aware object store for package and build infrastructure.
- A checkpoint/dataset/snapshot storage engine with strong exact revision economics.
- A content-addressable replication/transport substrate and partner backend.
- A hybrid local hot tier plus deeply optimized cold tier, or another architecture actually supported by results.
Assess the physical single-node limits, recovery/security gaps, compatibility, implementation complexity and cost to qualify each option. The first production release remains undecided. Do not declare artifact archiving the only possible initial product just because it performed best before this new research program. Equally, do not claim general-purpose superiority without independent evidence.
C19-D. Integrated acceptance and next roadmap
Run focused and targeted -race tests for changed concurrent subsystems, bounded actual recovery cuts for new publication boundaries, exact restore for all benchmark families, appropriate old-binary refusal, and one final root-suite invocation or honestly partitioned equivalent. Do not repeat all historical T1 stages or multi-TB qualification. Recompute tables from raw rows, verify all source digests and report remaining unvalidated claims. Keep C19 README ≤90 lines, with a final cross-program technical synthesis ≤120 lines in testing-harnesses/competitive/POST_C19_DECISION.md (not a replacement development diary). State whether any remaining major bottleneck suggests C20 or another series.
3. Optional C20 frontier reserve: one deep, high-risk hypothesis
Only after C19 or when a measured dependency makes it clearly useful, if authorized session capacity remains, select one high-upside question that C10–C19 did not resolve. Do not begin a random codec shopping list. Examples:
- Joint global compression graph solver: choose canonical/deep streams, multi-base deltas, shared dictionaries, and physical placement in one bounded optimization problem, with a provable or measured approximation gap on tractable graphs.
- Exact content-aware deduplication across structurally unrelated objects: learn deterministic reusable reversible transforms from bounded groups of authorized objects, using only exact bytes and a fully priced model; no probabilistic loss or private data disclosure.
- Predictive access-aware representation: use bounded measured reads and explicit SLA/storage budgets to choose the right archived/cached/reconstructed form with safe evolution under workload drift.
- Remote-first lossless cold tier: keep the compact shared canonical graph on a remote store while reconstructing only needed bytes locally, with exact proofs, explicit WAN latency pricing and no new billable backend requirement.
- Strongly bounded convergent maintenance: devise scheduling/placement invariants allowing foreground service and continual reclamation without ever requiring a large full-store rewrite on representative workloads.
For the selected hypothesis, preregister why the incumbent fails, the strongest competing design, input families, budget and an explicit PROMOTE/SPECIALIZED/RESEARCH_ONLY/KILL decision. Spend real research effort on one difficult concept, including a redesign when warranted, rather than superficial prototypes of all five. Keep final testing-harnesses/competitive/c20/README.md ≤90 lines.
4. Batch-by-batch acceptance and validation matrix
Every implementation edit: build and run focused tests for the touched subsystem; inspect diffs and resource behavior. Regenerate the existing source map if its contents require it. Keep comments short and avoid giant functions, cloned codecs, or unnecessary generic layers. No arbitrary LOC limit when an algorithm is substantive and maintainable.

Each meaningful checkpoint: an exact small reproduction, a negative control, the relevant authority/representation/transfer/maintenance suite, and a compact performance sanity check. Do not re-run B8–C9's entire experiments after every change.

New representation, dictionary, on-disk index, or durable format: explicit version and feature gate; old-reader refusal or compatible fallback as appropriate; deterministic fault injection; bounded actual SIGKILL cuts; fresh-process exact reads, deep verification, GC, revert, repair, snapshot/bundle, and cross-version mixed-store behavior. Test bad offsets, corrupt digests, missing bases, cycles, huge/untrusted claims, and bounded allocation.

Reader/cache/concurrent code: targeted -race on shared ownership paths, invalidation, stale reader and compaction, concurrent imports, cancellation and shutdown. Cache contents must be derived from verified identities, not unverified hints.

Network/protocol change: signed capability negotiation, scope isolation, old-peer fallback, exact SHA-256, request budgets, interruption/retry convergence and no private-scope disclosure. Ordinary S3 remains unchanged.

Final per-batch integration: one root Go suite with an adequate timeout when practical. If it is too expensive, run disjoint resumable partitions without overlap or unexplained gaps and state exactly what passed. Do not silently label a timed-out invocation as complete. No automatic full T1/36-stage or physical million-object qualification. Capture only actual commands and results.

For large expensive pipelines, set explicit per-batch disk/CPU/time budgets based on actual environment. A candidate that requires 50 GiB scratch on an 8 GiB sandbox must be redesigned or reduced, not forcibly run. Preserve the ability to stop and resume safely. Do not convert low disk capacity into an arbitrary limit on research ambition; test the relevant mechanism at a smaller physically affordable scale and state the extrapolation gap.
Verdict scheme
Use C10_READ_EFFECTIVE, C11_QOS_EFFECTIVE, C12_DELTA_EFFECTIVE, C13_DICTIONARY_EFFECTIVE, C14_CROSS_DOMAIN_EFFECTIVE, C15_INDEX_EFFECTIVE, C16_TRANSFER_EFFECTIVE, C17_STREAM_EFFECTIVE, C18_RECOVERY_EFFECTIVE, and C19_FRONTIER_EFFECTIVE only if substantive evidence supports the named advance. Use SPECIALIZED, RESEARCH_ONLY, NO_GENERAL_WIN, INCOMPLETE, or another truthful suffix when broader gains are absent. A batch that answers a difficult question negatively can be complete; a batch that omits required correctness is not.
Deliverable checklist for each batch
1. Verify branch, exact starting ancestor, final head, commit identity/trailers and unchanged main.
2. Write a brief preregistration and a ≤90-line testing-harnesses/competitive/cNN/README.md with reproduced evidence and actual caveats.
3. Include source hashes, pin versions, generators, replay driver, raw JSONL/CSV and a concise summary schema. Do not commit huge corpora, binary caches, credential values, or unneeded generated plots.
4. Compare independent controls against the strongest appropriate baseline using matched finalization, read-cache state, network path and durability assumptions.
5. Verify stored/reconstructed object bytes and any application-specific consumer semantics. Never substitute reconstructed content equivalence for byte-exact file identity.
6. Preserve S3 defaults and fail-closed behavior, unless separately documented and empirically justified. Record new format and safety behavior.
7. Document both strong positive and important negative results, efficiency costs, unsupported claims, omitted tests and next dependencies.
8. Commit and push all coherent checkpoint work, then proceed to the next batch if resources and user authorization permit.
Final campaign completion
After C19, or after optional C20 if undertaken, write a compact final campaign overview under testing-harnesses/competitive/POST_C19_DECISION.md, verify all completed report paths, and provide a 25–40-line chat response with a table of actual verdicts, strongest measured improvements, key regressions, remaining qualification limits, and recommended next frontier. Pause or retire any user-authorized automation associated with a completed program only if the scheduling controls actually permit the agent to do so; never imply an automation was changed without confirmation. Do not schedule new recurring work merely to continue this queue.

START NOW: Fetch research/next, verify 3cf73e37205c98525686fbf470756dc4a2e46c66 as an ancestor and main unchanged, inspect C9/C8/C7/C6 findings and the existing compressed reconstruction code. Start C10 exact full-read reconstruction with controlled profiling and at least two serious alternative designs. Continue sequentially through the remaining queue with verified checkpoint commits and independent evidence. This is an ambitious research program, not an artifact-archive-only release plan, and not a request to inflate documentation or consume usage without measurable progress.