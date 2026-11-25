# C1 preregistration

Baseline: ZeroS3 dcefbcb5c2539c74a0d33bfc0d6e4a380f671743, Go 1.27.0.
Primary loopback S3 candidates: ZeroS3, RustFS 1.0.6, SeaweedFS 4.55,
Garage 2.2.0, MinIO RELEASE.2025-09-07T16-13-09Z.
History candidates: Restic 0.20.0, Kopia 0.23.1, Borg 1.4.5,
OpenXet 11c7fea1e4d651365755432b67964d7ed4b53fea and the same S3 servers
using immutable version keys. Assess local Xet with the official client.
Hosted: Hugging Face public bucket and public dataset, official
huggingface_hub 2.4.0 and hf_xet 1.8.0. Report hosted separately.
Pinned installed tooling and failures will be recorded alongside results.

Twelve compact deterministic histories cover boilerplate, JSON, compressed
independent assets, source trees, native binaries, package and OCI-style
archives, SQLite, tensor data, insertion shifts, exact repeats, and entropy
controls. Record file SHA-256 and sizes before executing comparisons.
Use ordinary S3 objects from 1 KiB to 1 MiB per operation, including
compressible documents and high-entropy binary. Short fixed-count trials
characterize this VM, not service capacity. Mark synthetic fixtures as such.
Parquet, SST and external source release histories are expansion lanes.

S3: single node, no TLS on loopback, fresh store per repeat. Keep
product durability defaults and record process count and storage backend.
Do not infer crash or power-loss durability from restart checks. Start with
a basic interoperability smoke test, then run PUT, GET, range GET, HEAD,
LIST and DELETE, and assess multipart and mixed traffic where supported.
Use fixed operation counts and concurrency 1, 4 and 8. Verify full objects
and range payloads. Report nearest-rank percentiles and measured client
throughput, plus CPU, RSS and filesystem allocated bytes where supported.
Do not report p99 for fewer than 100 observations in an operation group.
Use deterministic rotating system and concurrency order over three repeats.
Report repeat medians and ranges. Keep warm/kernel-cache-eligible results
labeled. Cache dropping and traffic shaping require unavailable capabilities.

Histories: ingest ordered revisions, sample physical blocks after each,
run normal synchronous maintenance where supported and separately account
for its time, then restore and hash the retained versions. Compare equal
history depth. Measure reclamation after removing an old version where
supported. Record encryption, compression, indexes and redundancy. Compare
default ZeroS3 and offline maintenance/representation profiles separately.
Three independent repetitions for major performance lanes, with any omitted
repeats reported as reduced coverage.

Hosted: use only newly created storage_test destinations and a C1 prefix
within them. Cap logical uploads at 1 GiB and aggregate measured transfer
at 3 GiB. Use exact repeat, small edits, shifted and unrelated data, capture
client/transport observations where possible and verify SHA-256 of all
downloads. Compare fresh and reused client caches, distinguish forced
network fetch from a local cache hit, and record network conditions.
Hosted CAS allocation is unmeasured. Never compare raw WAN and loopback
latency as equivalent. Do not use automatic cross-resource discovery.

Scratch budget 4 GiB inclusive of installation and build artifacts, stage
one server/store at a time and reclaim owned restores and stores. Maintain
at least half of starting 60 GiB free. Check peak use and block accounting.
Exclude failed, timed-out or unverified operations from performance
summaries. Preserve error counts and invalid configurations in raw evidence.
Label unsupported, blocked and unmeasured surfaces explicitly.
