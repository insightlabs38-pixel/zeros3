ZeroS3 Post-C2 Frontier Queue v1.0
One master execution prompt for sequential, resumable Codex development: C3–C9
Repository: https://github.com/insightlabs38-pixel/zeros3
Development branch: research/next
Required starting ancestor: 4bc66f3af3d1d97f90c483c2b26676d3912fbb02 (C2_PERF_EFFECTIVE)
Stable production reference: main at last verified 2ac9cab (verify current state, do not modify it)
Frozen external comparison points: B10 dcefbcb, C1 fe14dfd2de3915852e143aa4c557f5596bdb47e9, C2 4bc66f3
Working model: one sequential agent, meaningful checkpoint commits, no per-subtask PRs, no unbounded physical-storage qualification.
Recommended reasoning: High. Execute the queue in order unless measured dependencies require a documented change.
0. Mission, ambition, and boundaries
ZeroS3 is a pre-1.0, single-node, S3-compatible, zero-external-dependency Go storage engine. Its research branch has already explored CDC/CAS, representation authority, global optimizer graphs, canonical decoded streams, grouped records, executable normalization, exact replay, nested deep streams, live maintenance, and extensive recovery testing. Do not rediscover or reimplement these features. Read the current source and research reports before designing replacements.

The next frontier is systems-level utility: efficient transfer of representation-aware changes, durable continuous operation, global physical storage economics, incremental similarity indexing, streaming reconstructions, and performance in real consumers. A format parser that merely recognizes more extensions is not a frontier improvement. A large implementation is acceptable when it establishes a genuinely new primitive and measured value.

The C2 result informs every following batch:
- C2's strong full-finalization results came primarily from correcting benchmark maintenance, not a wholly new compressor. Its finalized C1 composite was 28,295,168 allocated bytes, the same as fully finalized frozen B10.
- Parquet sparse 4 KiB reads improved from 88.67 to 4.08 ms in matched tests; broader cold-read gains remain unproven.
- Full first-pass optimization still costs around 11 seconds on the C1 composite; candidate generation dominates. The opt-in completed-pass memo reduces unchanged reoptimization to about 0.052 seconds, but is disposable and cannot validate data integrity.
- Four-client ordinary S3 PUT/GET measurements on the tested VM were 5–10% lower than the frozen B10 comparison, with cause not isolated.
- The full root suite timed out at 18 minutes; the resumed remainder reported zero failures. Do not describe this as an uninterrupted complete pass.
- OpenXet's pinned local client/server pair failed one tensor-edit reconstruction in C1. Treat it as a recorded interoperability defect, not a valid performance datapoint, and do not generalize the failure to Hugging Face production.
- On a matched local Xet trace, exact 4 MiB repetition transferred about 7 KiB through Xet but the ordinary ZeroS3 S3 PUT sent the full payload. This says something about interface capabilities, not that ZeroS3's existing native CDC protocol was benchmarked fairly against Xet.
The goal is to make ZeroS3 materially better, not to produce a string of optimistic labels. Negative results and killed approaches are first-class research output. Where the evidence invalidates a plan, pivot intelligently without making the next batch a superficial substitute.

Do not change main. Do not change the public maturity claims to pretend research-only functionality is production qualified. New persistent formats must be versioned, refuse unsupported readers, and preserve the existing data guarantees. Preserve Go stdlib-only production runtime and the single-file zeros3.go organization unless a compelling, measured reason requires a user-approved change. Test harnesses can use Python, Rust, containerized competitors, and external benchmarks.
1. Overnight/resume execution contract
This is a single sequential queue, not a set of simultaneously executing agents. Work independently through C3, C4, ... C9 as capacity permits. The user may restart or continue the same queue across usage resets and nonpersistent workspaces. The repository, not local scratch or the chat transcript, is the durable record.

At the start of every session:
1. Fetch remote refs; inspect research/next, main, Git status, last commits, and pending changes. Preserve legitimate newer work rather than resetting to the original SHA.
2. Locate completed batch reports and machine-readable verdicts in testing-harnesses/competitive/c3 through c9, as applicable. Inspect last checkpoint commit and the next work item.
3. Read relevant source, tests, and results. Avoid rerunning an expensive benchmark already captured with valid inputs unless changes require it.
4. Check environment CPU, memory, free disk, filesystem, and available tools. The environment can differ each time. Never assume the prior scratch directory survived.
5. Resume the first unfinished checkpoint of the current batch. Do not recreate completed experiments, overwrite valid result files, or rerun C1/B8–B10 campaigns by default.
6. If interrupted mid-experiment, distinguish committed evidence, reproducible but not-yet-run work, and invalid partial results. Commit only coherent tested changes; preserve useful scripts and manifests.
7. Continue through subsequent batches without asking for approval after each checkpoint, provided the actions are inside these instructions and existing resource/credential authorization. Do not continue past an explicit user stop instruction or a safety-critical ambiguity.
8. Do not invent time estimates, completion states, benchmark numbers, test passes, or supported features.
At each natural checkpoint, push a concise commit. Target approximately 2–5 substantial commits per batch, more only if justified by real independent changes. Keep commit subjects informative and commit/PR descriptions under ten lines. Do not create an isolated PR for every checkpoint. Direct research commits go to research/next; main remains untouched.

Git identity for both author and committer:

insightlabs38-pixel <insightlabs38@gmail.com>

No Co-authored-by, model/tool attribution, generated-by trailers, session links, or platform-appended footers. Inspect commit metadata after pushing. Avoid destructive rebases or force pushes. Never commit secrets or large original corpora.

Report discipline: each batch's README.md is at most 90 physical lines, ideally 60–75; validate with wc -l. Small PREREGISTRATION.md or a technical decision record may be longer when genuinely necessary, but do not evade the report limit by moving a 300-line diary into another Markdown file. Store tables, telemetry, and raw trials as JSONL/CSV, generated from reproducible scripts. Final chat status for each completed batch should be 20–35 substantive lines.

A user-facing completion report names the exact head SHA, commits, measured gains and losses, verification actually performed, unresolved limitations, decision for the next batch, and whether main was left unchanged.
2. Research quality and resource controls
2.1 Evidence hierarchy
Prefer measured materialized stores and real reads to estimates. Prefer independent artifacts and held-out histories to synthetic constructions tailored to a new representation. Show at least one negative control. Distinguish logical history bytes, retained physical allocated bytes, peak bytes during maintenance, transferred application payload bytes, actual wire bytes, and compressed data produced by the client. These are different metrics.

For read performance: distinguish first-touch/process-cold, kernel-cold (only if actually established), warmed, cache-hit, and repeated-neighbor accesses. Track throughput and p50/p95/p99 under controlled concurrency, plus client/server CPU and RSS. Claims of superiority must survive variance and appropriate same-hardware comparisons.

For transfer: distinguish bytes shipped by client to server, bytes consumed by internal dependency reconstruction, bytes uploaded or relayed by peers, local disk I/O, and bytes finally retained. Inspect actual protocol framing. A transfer that ships 10 KiB but reconstructs 4 GiB should not be called cheap.

For storage: finalization must be comparable. C2 established why gc alone is not equivalent to gc → repack (appropriate full-live settings) → compact → gc. Measure maintenance separately and verify live objects afterward. Do not repeatedly execute expensive full-store finalization when a focused measurement suffices.
2.2 Resource budgets
Discover actual limits every session. Target moderate reproducible fixtures, usually 1–512 MiB each. Keep multiple related revisions without requiring many GiB simultaneously. Use sequential corpus/store staging, a scratch quota, and an emergency free-space margin. Never require multi-TB disks, physical million-object stores, privileged cloud infrastructure, or long-scale qualification merely to proceed. If an index-scale experiment needs a million keys, use bounded tiny payloads and measure metadata separately; do not materialize millions of large blobs.

External hosted testing is optional and must stay within already-authorized disposable resources. Do not require a live Hugging Face credential for routine C3–C9 work. If one is present, use an environment-secret mechanism, existing authorized bucket/prefix/repository only, controlled upload/download caps, redact all logs, and leave unrelated resources unchanged. Never infer backend physical CAS allocation from transfer counters. Do not create billable resources without explicit authorization.
2.3 Correctness floor
Even in research mode: every stored/recovered object must be byte-exact, not merely semantically equivalent. Preserve original SHA-256 identities, S3 visibility and metadata, ETag contract, conditional requests, namespace operations, and durable acknowledgment behavior. Reject malformed compressed input, cycles, offsets, unknown format features, missing bases, and corrupted metadata. No wrong-byte fallbacks.

Target tests continuously, root-wide suite only once per finished major batch where feasible. If full root takes too long, run disjoint resumable partitions with explicit accounting. The original C2 18-minute timeout should not be papered over, nor must every experiment wait for a 30-minute suite. Introduce real SIGKILL cuts only for new durability transitions and use targeted -race when concurrent ownership or caches change. Do not disable tests to get green. Do not repeat 36-stage T1 or multimillion-object scale qualification.
2.4 Quantitative stop/continue gate
Each batch begins with a short technical hypothesis and an experimental preregistration. For a candidate, compare against the strongest applicable incumbent, estimate incremental complexity and correctness risk, and measure an early representative lane. A lane can be PROMOTE, RESEARCH_ONLY, REDESIGN, or KILL. A bad first prototype may merit a targeted redesign; it does not deserve unlimited retries after evidence shows a fundamental cost wall. Conversely, do not abandon a genuinely promising frontier merely because its first implementation is technically difficult.

Do not substitute number of tests, LOC, or format count for measurable engineering progress.
C3: Representation-aware native transfer and global deduplication
Priority: Highest. Start immediately after checking the C2 baseline.
Thesis: A ZeroS3-aware client/server or peer-to-peer path can use verified canonical and structured components to transfer fewer bytes than generic CDC on difficult version histories, while ordinary S3 clients continue to work unchanged.
C3-A. Inspect actual prior protocol and research
Read docs/ZEROS3_PROTOCOL.md, native CDC/bulk negotiation, current repair and replication paths, B3/B4/C1/C2 reports, and available Z3 structured-transfer work (Z3_ST*, Z3_STRUCTURED_TRANSFER_CAMPAIGN, drafts where present). Map which primitives already exist and which paths are laboratory-only. Do not start by designing an entirely new protocol.

Document how existing transfer handles: exact duplicate, insertion-shifted revision, a related base stored under a different S3 key, format-reencoded content, a remote peer with older protocol support, interrupted upload, and transfer to a receiver without the desired base.

Review official Xet documentation on chunking, xorbs, shards, upload negotiation, and reconstruction (https://huggingface.co/docs/xet/en/). Local OpenXet is an optional same-hardware comparator. Hosted Hugging Face must remain a separate WAN lane and must not determine C3 architectural conclusions.
C3-B. Pre-registered matched transfer comparison
Use identical inputs on sender and receiver, fixed CPU/memory/network, and frozen source digests. Workloads should include:
- Exact repeated 4–32 MiB objects, same key and different key.
- 4–32 MiB versions changed in place, insertion-shifted, and low-overlap.
- Small files aggregated into a revision and software source histories.
- TypeScript, Django, RubyGems, OCI compressed packages, related versions with nested content.
- Real producer-generated Parquet/SST snapshots.
- Model/Safetensors histories and a non-model binary control.
- High-entropy unrelated files and independent compressed archives.
Compare ordinary full S3 PUT, existing ZeroS3 native CDC transfer, a simple CDC+compression baseline, structured-transfer experiment, and Xet/local OpenXet where reconstructible. Vary recipient knowledge: empty destination, already has same object, has previous same-key revision, has related different-key object, and has only partial shared components. Separate cold analysis from reusable verified metadata.

Record network bytes including protocol overhead, number of round trips, client and receiver CPU, analysis time, RAM, temporary disk, read amplification, restart/retry costs, final allocated bytes, and SHA-256 of every restored object. Model network RTT and bandwidth with controlled local shaping when permitted; avoid fabricated WAN-equivalence numbers.

The test should answer whether structural transfer adds real value beyond an already effective native CDC baseline, not merely beyond full S3 PUT. Do not take credit for server-side deduplication as if it reduced wire bytes.
C3-C. Transient same-key and cross-key base bootstrapping
Investigate a receiver-assisted bootstrap using already authorized, related stored content. If the target receiver holds the prior object under an S3 key, it may provide verified chunk or structural hints to a ZeroS3-aware sender; if it holds related data under different keys, explore bounded candidate discovery that does not reveal unauthorized content.

Possible primitives: scoped inventory digests, salted or access-controlled component probes, short-lived dependency leases, verified sketch exchange, content-hash negotiation, missing-component request sets, sparse-source reconstruction, and a final verified commit through the existing namespace path. Reuse the existing native protocol's authentication. Do not introduce a cross-tenant hash-existence oracle: all reuse claims must be limited to the authenticated principal's permitted namespace or a trusted peer relationship.

Optimize first for genuine production-like use cases: replacing a model checkpoint, uploading the next software package revision, moving a changed dataset between two authorized ZeroS3 stores. Preserve generic S3 fallback without client changes.
C3-D. Structured component transfer
Explore transfer of exact representation components rather than only CDC chunks. Candidates include verified canonical decoded streams, delta residuals, nested deep-stream segments, restartable records, and a minimal reconstruction recipe. Avoid pushing a representation into a receiver that cannot understand its feature version. A receiver may instead reconstruct the exact target and commit an ordinary CDC object, preserving compatibility.

Compare two designs:
1. Transient reconstruction: sender describes a reversible target, sends only missing verified parts, receiver reconstructs and writes through ordinary CDC. No persistent dependency exposure is required.
2. Representation-native replication: a trusted receiver persists selected shared components and recipes, with explicit feature/version negotiation and dependency closure.
The first design is preferable if the second's persistent dependency bookkeeping, protocol complexity, and recovery costs are not justified. Stage incoming data transactionally, verify all required SHA-256 identities before publication, and avoid publishing a partially reconstructed object. Test a receiver crash between component writes, final reconstruction, and namespace commit.

Evaluate structural hints, not just whole encoded data. A canonical stream that saves storage may still be a bad transfer representation if sender decoding and receiver replay CPU exceed the network benefit. Record the break-even point over realistic RTT and bandwidth ranges.
C3-E. Native protocol semantics and security
Preserve existing ETag, metadata, object version identifiers, conditional semantics, LIST behavior, and acknowledged write durability. Protocol version negotiation must fail closed or downgrade to existing native CDC/full S3. Verify new-to-new, new-to-old, old-to-new, peer absent, missing base, corrupt base, hash collision simulation, interrupted request, timeout, and retransmission. Limit request and description sizes, recursion, decompressed expansion, and memory. Do not leak content similarity across unauthorized users.

Avoid whole-store scans on every transfer. Use existing indexes or a bounded temporary per-transfer index. If globally persistent similarity discovery is the limiting factor, record the requirement for C7 rather than implementing an uncontrolled metadata subsystem during C3.
C3-F. Verdict and gate
Deliver a functioning end-to-end transfer path if and only if it beats existing CDC in at least one meaningful independent compressed-history or shifted-revision class after CPU and request overhead are included. It may remain an opt-in native client feature. If structured transfer consistently loses to CDC, keep the measured research and improve the existing native transfer bottleneck instead of forcing a protocol extension.

Primary output: testing-harnesses/competitive/c3/README.md (≤90 lines), scripts and raw transfer traces in results/, compatible protocol tests, targeted crash/retry evidence. Classify C3_TRANSFER_EFFECTIVE, C3_TRANSFER_SPECIALIZED, C3_NEGATIVE, or C3_INCOMPLETE. Preserve exact network-byte evidence.
C4: Operational correctness, compatibility, and ARM64
Priority: Foundation for later aggressive physical and streaming changes.
Thesis: ZeroS3's advanced storage is useful only if users can recover, upgrade, migrate, and serve objects correctly under routine faults and standard clients.
C4-A. Isolate the C2 default-S3 throughput regression
Recreate the 5–10% change on matched B10 and C2 binaries, alternating run order, measuring enough operations to distinguish environmental noise. Profile the default PUT, random GET, range GET, request signing/checksumming, locking, per-request allocation, and page-cache interactions. Use matched CPU quotas and fresh versus warm runs. Explain whether additional code paths, metadata lookups, benchmark concurrency, or noise account for the observed difference.

Correct genuine regressions when possible without weakening correctness. If measurements overlap statistically, report the non-finding. This triage must be bounded and must not consume the entire C4 campaign.
C4-B. Recovery, maintenance, and format compatibility
Inspect B4 online GC, grouped-record stale views, C2 archive finalization, authority revert/migration, checkpoint/journal recovery, pack rebuild, old-bin feature refusal, and peer repair. Prioritize gaps introduced by new C3 transfer stages and C2 finalization. Test small but adversarial stores with deliberate process kills, corrupt metadata, incomplete group rewriting, missing component records, and concurrent readers during maintenance.

Define a minimal supported operational contract: initialize, serve, graceful shutdown, restart, offline verify, deep verify, archive finalize, repair, revert, export/snapshot, restore. Provide a one-command smoke/integration harness for that contract. Where native capabilities already exist, test and document them rather than adding commands.

Check that the full root suite can be completed or deterministically partitioned under a realistic time budget. Never claim the previous C2 timed-out run was a clean uninterrupted pass. A root test infrastructure improvement is valuable when it saves future campaign usage without dropping coverage.
C4-C. Real client interoperability
Select a compact set of common S3 clients such as AWS CLI/boto3, AWS SDK for Go v2, rclone, s5cmd, or MinIO mc (only those feasible in the environment). Validate signed operations, multipart, range, conditional requests, pagination, retry behavior, object metadata, list/delete semantics, and malformed requests according to ZeroS3's actual advertised Core Client Profile. Do not promise full AWS S3 parity.

Investigate one credible real workflow: backup to an S3 endpoint, artifact storage for a build runner, local OCI-compatible blobs, or ML checkpoint uploads. Choose by measured fit, not by adding a wide integration matrix with no users.
C4-D. ARM64 portability
Cross-compile the standard-library-only Go engine to Linux ARM64 using CGO-disabled toolchain settings, and identify architecture-specific assembly, unsafe memory layout, endianness, alignment, filesystem locking, and compression implementation assumptions. If native ARM64 hardware or emulation is actually accessible, run focused S3/authority/recovery/sparse-read tests. A successful cross-compile alone is a build result, not native runtime qualification.

Keep ARM-specific optimizations optional and verified. An ARM64 host can still store x86-64 ELF inputs, though the supported executable normalization input format is a separate concern. Do not claim native ARM binary normalization unless implemented and tested.
C4-E. Operator economics
Record startup, restart, backup/restore, basic integrity checking, online maintenance, resource bounds, and crash outcomes on small real stores. Identify the most important barriers for a small team operating a single-node S3 endpoint. Avoid designing multi-node consensus, IAM/STS, a web console, or a distributed control plane as a side project.

Deliver testing-harnesses/competitive/c4/README.md (≤90 lines), a reproducible smoke suite, measured S3 regression analysis, actual ARM64 qualification level, and a concise list of remaining genuine deployment blockers. Status C4_OPERATIONAL_EFFECTIVE, C4_PARTIAL, or C4_INCOMPLETE.
C5: Independent validation and commercial decision checkpoint
Priority: Compare the system after C3/C4 against strong independent alternatives, then choose the most valuable next research frontier.
Thesis: Excellent specialized benchmark numbers only matter if they survive deployment-equivalent semantics and realistic artifact workflows.
C5-A. Freeze a checkpoint and adversarially remeasure
Freeze a C5 baseline SHA. Reuse validated C1/C2 harnesses rather than rebuilding the entire competitor laboratory. Keep the original C1 and C2 results immutable. Repeat representative lanes on the same machine for ZeroS3 baseline and current ZeroS3; include two or three relevant independent systems, not every historical target.

S3 server lane: ordinary object PUT/GET/range/mixed workloads, concurrent latency and CPU. History lane: ordinary CDC versus archive versus mixed, with full comparable finalization and maintenance cost. Transfer lane: full S3, native CDC, C3 structured transfer, local Xet where a validated path exists, and an official hosted Xet lane only if already-authorized credentials and quota make it worthwhile. Hosted WAN remains separate.

Broaden independent real corpora: versioned package artifacts, small-file source history, independent model revisions, compressed Parquet/SST, nonmatching binaries, and low-overlap controls. Reserve a genuine held-out subset chosen before C3/C4 tuning.
C5-B. Storage, transfer, read, and compute Pareto frontiers
For each family, show allocated disk versus read cost versus ingest/optimization CPU versus transferred bytes. Establish break-even under explicit storage/CPU/bandwidth/retention assumptions. Do not convert a few-GiB local test directly into a petabyte pricing guarantee.

Determine whether the primary advantage is: S3-compatible storage with transparent CDC, native structure-aware synchronization, cold software-artifact archives, model/dataset version history, or specialized database snapshots. Compare the incremental value of ZeroS3 beyond a mature backup engine and beyond Xet-style client chunking.
C5-C. Real-world decision and revised queue priorities
Identify which parts of C6–C9 have the highest expected gain, calibrated to C5 measurements. You may reorder within those batches or narrow clearly losing lanes, but do not silently delete entire batches simply to avoid hard work. The campaign is intended to push architectural limits as well as improve immediate operations.

Deliver testing-harnesses/competitive/c5/README.md (≤90 lines), raw matched comparisons, a brief cost sensitivity model, and specific hypotheses for C6–C9. Verdict distinguishes effective specialized niches, unsupported opportunities, and weak hypotheses. Do not declare product-market fit from benchmark performance.
C6: Physical-store frontier beyond representation graphs
Priority: High-impact new research direction.
Thesis: After B9 grouped authority records and C2 archive finalization, the next major storage gain may come from global physical placement and compression, not another file-format transformation.
C6-A. Establish the actual global packing floor
Trace physical storage by class for small-file, compressed archive, Parquet/SST, tensor, source, and mixed histories: CDC blocks, authority records, grouped record segments, packs, index pages, free/slack space, metadata, and compression overhead. Compare current packing and compacting across size distributions and live/dead ratios. Measure actual read cost, write amplification, and crash recovery of the layout.

Audit earlier B5/B6/B9 pack, record group, and optimizer research first. Do not repeat B9's small-record grouping as a supposedly new concept. Seek residual physical inefficiency after fully correct finalization.
C6-B. Global content-aware packing and placement
Explore placing related small chunks/components together, rather than independently choosing per-object graph representations and then applying generic packing. Possible methods include correlation/sketch-based co-location, read-coherent blocks, shared restart units, immutable pack cohorts, small-object suballocation, selective recompression across neighboring objects, and controlled pack-size classes.

Evaluate: compression across version-neighbor components, amount of data touched by a sparse read, live-set fragmentation after deletion, costly rewriting of partly live packs, and reader contention during online packing. Use physical measurements, not abstract bit savings. Contrast packing by similarity with packing by read locality. The globally smallest encoding may not minimize read amplification.

Investigate whether a two-part objective (stored bytes and bytes fetched per typical read) outperforms current layout. Model write amplification and maintenance budgets explicitly.
C6-C. Shared compression context, dictionaries, and entropy structure
A fresh frontier worth testing is shared compression context across related objects or groups, where individually compressed fragments are too small to exploit recurring data. Consider dictionary groups, content-aware pack compression, shared literals/tables, or transform-generated families. Every represented object must remain byte-exact; avoid semantic-only deduplication. An experimental encoder may use external test tools, but the production reader/writer must remain stdlib-only unless the project's dependency rule is deliberately and explicitly changed by the user.

Do not adopt a new compression algorithm merely for breadth. Require real savings beyond C2's strongest generic delta and canonical/deep baselines, with CPU and sparse-read cost included. If codec development dominates the budget without a likely return, keep the result in a research-only harness and proceed to physical placement.
C6-D. Online storage geometry and reclamation
Investigate how pack layout evolves after many revision additions and deletions. Test a small fixed physical store over multiple churn cycles. Optimize read-stable re-layout, low-copy compaction, batch rewriting, group occupancy, and safe retirement. Reuse B4 reader leases and C2 finalization. New pack formats require versioned features, targeted crash cuts, and older-reader refusal.

Measure eventual allocation, peak staging, pack write bytes, p95 4 KiB read, concurrent reads, compaction time, CPU, and fragmentation after churn. Do not retain an enormous test history to demonstrate churn.
C6-E. Decision gate
Integrated promotion requires an independently measured materialized physical-storage improvement, or a substantial reduction in maintenance/write cost at equivalent final allocation, against fully finalized C2. A smaller theoretical representation graph is not enough. If global shared compression is uneconomic, retain the useful packing work and report the negative result.

Deliver testing-harnesses/competitive/c6/README.md (≤90 lines), benchmark scripts/rows, focused corruption/recovery tests, and a precise statement of whether a new persistent format was introduced. Status C6_LAYOUT_EFFECTIVE, C6_SPECIALIZED, C6_NO_WIN, or C6_INCOMPLETE.
C7: Incremental similarity indexing and metadata-scale architecture
Priority: Major unexploited scaling opportunity with direct relevance to C2/C3.
Thesis: An advanced content-aware engine cannot repeatedly rescan and reparse an entire store to discover one new revision's useful bases. It needs compact, updateable, verifiable discovery structures that do not become a new metadata bottleneck.
C7-A. Profile real discovery and namespace costs
Read B3 namespace-v4 sparse page/journal design, B6/B7 candidate discovery, C2 analysis memo, current object index, authority graph, repairs, and GC. Profile fresh optimize, no-change optimize, one-object revision, one deleted history, and many unrelated objects. Separate file scan, content fingerprinting, candidate ranking, persisted index lookup, metadata open/rebuild, and graph search. Measure RSS, I/O, CPU, wall time, and local/remote cache effects.

Study 1k, 10k, 100k, and, where feasible, metadata-only 1M entries with tiny physical payloads. Do not repeat a million large objects or terabyte-scale load. Distinguish synthetic metadata index scaling from complete S3 serving at scale.
C7-B. Design an incremental verified similarity index
Explore a compact index mapping content hashes, CDC sketches, format signatures, canonical/deep-stream identities, and similarity anchors to candidate bases. Potential approaches: sparse postings, hierarchical locality-sensitive fingerprints, feature-specific inverted lists, multi-resolution sketches, version neighborhoods, disk-resident sorted runs, and bounded in-memory caches. Pick a design based on measured memory and false-positive economics, not novelty alone.

Require bounded update cost for a new object, efficient selective invalidation, crash-safe incremental publication, repair/rebuild after corruption, and safe fallback to full discovery. An index is a hint: false positives may waste CPU, but must never authorize wrong reconstruction. False negatives reduce savings and should be characterized on held-out histories.

Coordinate with C3: authorized native transfer may benefit from a compact receiver-side inventory or similarity hint, but no unauthorized global hash-oracle API should be exposed.
C7-C. Durable metadata geometry
Investigate whether current namespace journaling, checkpointing, and page indexing scale efficiently with churn. Target read/write amplification, update fanout, index footprint per object, small-object GET/HEAD/LIST p95, reopen/recovery time, and compaction. Avoid speculative sweeping rewrites of namespace v4 unless real profiling establishes a bottleneck. Measure effect on C2 default-S3 throughput.

Use write-ahead or publish-then-index arrangements consistent with existing authority safety. New durable index formats must have version features, checksummed structure, deterministic rebuild, bounded scan progress, and targeted process-kill tests. An index corruption must not silently drop committed objects.
C7-D. Incremental optimizer integration
A small new revision should discover strong related bases without rebuilding every candidate for the entire store. Explore incremental dependency subgraph updates and partial reoptimization with valid incumbent plans. Preserve global sharing guarantees to the extent practical; explicitly measure the quality gap versus a full scan and full optimize on the same store. Limit persistent index size and memory.

Show at least one case of substantially reduced new-version analysis cost at comparable retained storage, not just C2's already-fast no-change memo hit. Include negative cases with unrelated data and adversarial similarity collisions.

Deliver testing-harnesses/competitive/c7/README.md (≤90 lines), bounded scale curves, index byte/object, crash/rebuild evidence, and held-out candidate-quality results. Status C7_INDEX_EFFECTIVE, C7_SPECIALIZED, C7_NO_WIN, or C7_INCOMPLETE.
C8: Streaming ingestion, reconstruction, and I/O scheduling
Priority: Systems frontier for reducing amplification across the full data path.
Thesis: Offline graph optimization can save bytes but still be expensive because ingestion, candidate construction, materialization, pack rewriting, and range decoding duplicate I/O. Stream verified changes through fewer intermediate copies while keeping the default S3 fast path intact.
C8-A. Trace the full data flow
Instrument ordinary PUT, native C3 delta transfer, optimistic candidate generation, canonical/deep-stream decode, authority publication, GC/repack/compact, and eventual GET. Record copies, allocations, checksum passes, temporary files, fsyncs, physical reads/writes, CPU, and critical-path dependencies. Consider low-RAM, few-core and constrained-disk environments as first-class targets.

Do not assume zero-copy is automatically faster. Prove material bottlenecks and keep the ordinary CDC path measurable throughout.
C8-B. Bounded streaming representation construction
Investigate streaming canonical/deep components and delta candidates directly into verified durable containers without materializing every full decoded stream on disk. Candidate ideas: chunk-wise transform pipelines, incremental SHA and CRC, bounded restart-unit buffers, content-aware splice boundaries, temporary immutable segments with atomic publication, multi-output derivation from one decoding pass, and reusing verified decoded bytes across candidate encoders.

B10 stored each deep stream as a temporary body during planning, with large temporary writes; C2 still found candidate construction dominant. Target a genuinely lower peak write and scratch footprint at identical final archive allocation and exactness. Avoid committing unverified partial components. On cancellation or SIGKILL, abandoned staging must be safely reclaimable.
C8-C. Shared reconstruction planning and scheduling
Explore compiling a representation graph into a bounded read plan: coalesce overlapping dependency reads, batch adjacent ranges, parallelize independent verified branches, reuse intermediate restart state, and avoid recursive rereads. Consider p95 under 4–16 concurrent users and multiple read sizes. Retain per-object integrity checks and reasonable memory bounds.

Benchmark nested gzip, Parquet, SST, generic delta, mixed source, and unrelated controls. A new scheduler must not make whole reads dramatically slower in exchange for one narrow 4 KiB case. Show latency/throughput/RSS tradeoffs.
C8-D. Ingest and maintenance coexistence
Investigate bounded admission/backpressure for background analysis or online maintenance, scheduling CPU- and I/O-heavy work so a busy S3 server remains responsive. Reuse current single-writer/reader-lease invariants. Observe p95/p99 ordinary S3 latency while archive finalization or native transfers run. Model reasonable priority classes: foreground GET/PUT, interactive transfer, background candidate analysis, compaction, verification.

Do not introduce an unbounded background daemon or long-lived worker fleet just to satisfy the concept. Where simpler explicit maintenance scheduling gives similar benefit, use it.
C8-E. Decision gate
Require measured improvement in at least one of: peak temporary disk, total bytes written, first-pass analysis wall/CPU, or read latency under controlled contention, without wrong bytes or unacceptable impact on default S3. If streaming representation assembly proves structurally incompatible with exact replay or safe publication, preserve a focused negative prototype and pursue read-plan coalescing or maintenance scheduling instead.

Deliver testing-harnesses/competitive/c8/README.md (≤90 lines), instrumentation and Pareto results, bounded race/crash tests around newly introduced publication stages. Status C8_STREAM_EFFECTIVE, C8_SPECIALIZED, C8_NO_WIN, or C8_INCOMPLETE.
C9: End-to-end integration, final competitive frontier, and architectural decision
Priority: Integrate the best proven research into realistic workflows and decide what deserves further investment.
Thesis: An advanced object store is only differentiated when a complete application path benefits from its storage, transfer, and read behavior under safe operational conditions.
C9-A. Choose representative consumers
Select two independent, concrete workflows based on C5 findings, ideally one from versioned software/build artifacts and one from ML/dataset or database snapshots. Candidate consumers include a build cache, registry/blob store, local checkpoint uploader, dataset artifact manager, or backup application using a real S3 SDK. Prefer a minimal integration with existing open-source clients over creating a large UI or framework. Do not imply upstream compatibility that was not tested.

For each workflow, test initial ingest, revised upload, repeat/exact upload, listing, partial retrieval, full retrieval, deletion/retention, integrity verification, restore/restart, and failure handling. When native C3 transfer is available, compare it to unchanged S3 clients in the same application.
C9-B. Rebenchmark against independent systems
Repeat the best-controlled subset of C1/C5: selected S3 servers for request speed, mature snapshot tools for retained history, local validated Xet for chunk transfer, real Hugging Face hosted service only if separately authorized. Freeze versions, label hardware/network/cache, and keep baselines independent. Evaluate static and active-workload tradeoffs.

Do not report a single composite leaderboard across S3 servers, backup systems, and WAN Xet. Use separate comparisons and Pareto curves. Record physical final allocation, write amplification, restore/read CPU, network bytes, operator effort, and failure outcomes.
C9-C. Final architecture and commercial judgment
Identify whether ZeroS3's strongest defensible role is: transparent S3-compatible CDC, version-aware build artifact store, native structured synchronization service, cold compressed-history archive, ML/dataset revision storage, or another measured niche. Explain which advantages are intrinsic to ZeroS3 and which can already be achieved by standard backup/Xet/CAS software. Include license, single-node limits, credential model, durability qualification gaps, feature parity, and maintenance economics. Do not claim product-market fit without users.

Prioritize one or two future architectural frontiers with measured reason to proceed. Examples: global compression across whole repositories, cross-store transfer index, safe larger-scale online compaction, ARM64 native performance, or a proven protocol compatibility gap. Distinguish research-only capability from production qualification and do not merge research/next into main automatically.
C9-D. Final acceptance and continuity
Create testing-harnesses/competitive/c9/README.md (≤90 lines), reproducible final result manifest, and testing-harnesses/competitive/POST_C2_CAMPAIGN.md (≤120 lines) summarizing C3–C9 results, accepted/rejected hypotheses, unresolved defects, supported deployment maturity, and next experiments. Confirm no false report of full root pass, native ARM64 support, or power-loss safety. Confirm main unchanged. Present final head SHA and explicit stop/continue recommendation.

Verdict categories: C9_INTEGRATED_EFFECTIVE, C9_SPECIALIZED_VALUE, C9_RESEARCH_ONLY, or C9_INCOMPLETE. The verdict follows measurements, not feature count.
3. Optional frontier reserve, only after C9 or a proven dependency
If C3–C9 finish early and there is substantial unused agent capacity, do not begin an arbitrary new file-format sweep. Select one tightly specified frontier probe backed by evidence in completed results. Candidate reserve directions:
- Cross-repository compressed-history sharing: explore whether shared canonical dictionaries or compact similarity indexes offer savings across independently generated package families beyond generic chunk dedup, with authorization-safe discovery.
- Read-aware physical graph materialization: automatically select between an archival representation and a cheap selectively materialized access path, with verified hotness and a storage/latency budget, without maintaining duplicate whole-object copies by default.
- Incremental transfer under weak receiver knowledge: derive bounded shared content across peers without scanning or leaking the peer's entire store, using safe summaries and local verification.
- Metadata-index locality under churn: test whether a compact immutable or log-structured index can reduce random I/O after substantial namespace change without another full-store reconstruction.
Pre-register the hypothesis, strongest baseline, test inputs, stop criterion, and resource budget. A research-only negative finding is acceptable. Do not claim all four directions were explored. Place the result under testing-harnesses/competitive/frontier-reserve/ with a ≤90-line report and link it from the final campaign summary.
4. Testing cadence and checkpoint matrix
Every edit: targeted build and test for the touched subsystem; ordinary static checks/formatting.
Each substantive checkpoint: relevant representation/authority/transfer/maintenance tests, a small exact reproduction, and a limited regression lane.
Every new durable publication or storage format: deterministic interruption points, a bounded actual SIGKILL matrix, fresh-process reopen, exact reads, deep verify, and convergence.
Concurrent reader/cache/index change: targeted -race, concurrent operations, lock/deadlock and cache-identity tests.
One integrated run per batch: a root suite if resource/time permits, or disjoint documented partitions with honest completion status. Do not run full T1/36-stage qualification, million-object stored-value tests, or entire C1/B8–B10 campaigns after every small change.

For server speed, preserve B10 and C2 pinned binaries. A 5–10% throughput shift on a two-CPU VM may be noise or client contention. Alternate trial order and use sufficient operations before claiming a regression. Favor p95/p99 and CPU explanation over ranking based on 144-request lanes.

For recovery, never modify source tests merely to avoid a failure. Tests that legitimately require legacy record layouts should explicitly set them, and tests for newer layouts should be analogous. Maintain byte-exact object behavior and never substitute a valid but different compressed byte stream.
5. Global admission, portability, and security constraints
- No destructive mainline changes. All implementation remains on research/next; main is the stability reference. Do not auto-promote features or open release PRs.
- Default safety: new expensive representations, network transfer protocols, global index layouts, and auto-maintenance behavior remain opt-in until targeted evidence and compatibility justify broader use. Preserve existing default S3 throughput and ordinary CDC fallback.
- Network security: do not expose content hashes or similarity across unrelated authorization scopes. Never trust sender-provided dependency claims without verified content identity and authorized access.
- Persistent format: explicitly feature-gate new formats, handle mixed older records, safe recovery, refuse unsupported readers, and keep migration/revert/reclamation paths coherent.
- Resource safety: bounded memory, recursion, decoder expansion, temporary disk, network bytes, CPU, object count; no unbounded cloud bills. No multi-TB requirements.
- Performance: actual allocated bytes versus modeled closure, wire bytes versus dedup bytes, process-cold versus kernel-cold, and single-node versus hosted WAN must never be confused.
- Language/runtime: Go standard library in production, no broad dependencies; ARM64 requires actual native/emulated runtime proof before qualification claims.
- Documentation: keep each README ≤90 physical lines, final campaign report ≤120; raw data and executable scripts contain the detail.
- Code quality: no arbitrary LOC caps, but avoid unnecessary abstractions, excessive functions, cloned algorithms, giant unreviewable generated docs, and inefficient hot paths. Preserve source-map generation and existing test organization.
- Git discipline: author and committer insightlabs38-pixel <insightlabs38@gmail.com>, no attribution trailers, short commits, validated checkpoint pushes, nonpersistent environment resilience.
6. Campaign completion checklist
On finishing each batch, before proceeding:
1. Confirm the exact Git head, branch, parent/ancestor, commit authorship, and unchanged main.
2. Check that code builds, focused tests pass, and any failed/timed-out broader suite is explicitly documented.
3. Recompute every claimed materialized size or performance summary from the raw measurements.
4. Verify corpus SHA-256 and byte-exact reconstruction of every required object.
5. Separate improvements due to new algorithms from benchmark changes, configuration fixes, and maintenance settings.
6. Confirm resource budgets, cleanup of scratch, credential hygiene, and no accidental large corpus commits.
7. Run wc -l on the batch report; keep it ≤90 physical lines.
8. Record successes and negative findings, residual bugs, incomplete claims, and the next batch's assumptions.
9. Push the checkpoint, then continue to the next planned batch if the session permits. No repeated permission request is needed for work clearly authorized here.
Start at C3. First verify the C2 head and identify the actual native transfer protocol rather than implementing an imagined one. Then progress sequentially through the queue, using evidence to select techniques and preserving all validated work in Git. The objective is substantial new engineering capability and truthful comparative results, not merely consuming usage or accumulating LOC.