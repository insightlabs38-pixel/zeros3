# ZeroS3 C1 independent storage benchmarks

**C1_PARTIAL.** Frozen ZeroS3: `dcefbcb5c2539c74a0d33bfc0d6e4a380f671743`.
Hosted coverage includes both authorized public resources. Local Xet
coverage has one repeatable reconstruction failure.

Debian 13 x86-64 VM, 2 CPU quota, 8 GiB cgroup memory, overlayfs, 4 KiB blocks,
and 60 GiB starting free space. This is a single-machine, kernel-cache-eligible
study. S3 trials use signed HTTP on loopback and SHA-256 verification.
Coverage gaps include Warp, kernel-cold reads, crash durability and repeated
hosted sequences. The primary history contains 12 synthetic workload families.
[Preregistration](PREREGISTRATION.md) · [Replay](REPRODUCE.md) · [Schemas](RESULTS_SCHEMA.md) · [Diagnostics](DIAGNOSTICS.md)

## Loopback S3

Four concurrent clients, 144 operations per lane, three independent repeats.
Values are median operations/s from the original configurations.

| System | PUT | Random GET | 256-byte range GET |
|---|---:|---:|---:|
| ZeroS3 | 601 | 991 | 1,046 |
| RustFS | 423 | 906 | 943 |
| MinIO | 513 | 792 | 926 |
| SeaweedFS | 471 | 843 | 915 |
| Garage | 188 | 224 | 106 |

All five passed multipart smoke, listing, HEAD, mixed traffic and delete.
ZeroS3's PUT range was 411–627 ops/s. Random GET ranges overlapped.
Garage used synchronous data and metadata. Its 256-byte probe measured 44 ms
with a reused connection and 2 ms with a fresh connection. SeaweedFS reported
a volume-allocation error in its original two-volume history configuration.
Its eight-volume retest reached a median 440 PUT ops/s at four clients.
Durability and maintenance differ. Power-loss equivalence is unverified.

## Three-revision histories

Median allocated MiB for 89.80 MiB logical, with three fresh runs per lane.
S3 used immutable revision keys. Backup engines used actual snapshots.

| Storage interface | Allocated MiB |
|---|---|
| S3 objects | ZeroS3 default 40.35, Garage 47.23, SeaweedFS 82.92, MinIO 96.09, RustFS 96.18 |
| Backup snapshots | Borg 38.55, Restic 43.09, Kopia zstd-fastest 44.19, Kopia default 58.50 |
| ZeroS3 offline | Archive 35.57, mixed 35.94, hot 40.41 |
| ZeroS3 inline | Representations preferred 52.77 |

Archive saved 11.8% over ZeroS3 default, with an 11.77 s median optimization
and GC pass after 1.92 s ingest. Median verified full reads rose from 1.04 s
to 1.25 s. The optimizer used up to about 61.3 MiB of sampled store allocation.
Borg and Restic overlap the default ZeroS3 footprint. Snapshot reconstruction
writes to disk, and its timing is presented separately from S3 GET.
Single-run archive cut tensors to 2.11 MiB versus 2.34 for generic deltas,
but grew lodash from 0.98 to 4.48 MiB versus default CDC. Generic deltas
matched archive on SST and used 1.92 MiB versus archive's 2.50 on Parquet.
[Full tables](results/tables.md) · [Storage and timing chart](results/history-tradeoffs.png)

## Xet

OpenXet loopback used official huggingface_hub 2.4.0 and hf_xet 1.8.0.
A 4 MiB new upload used about 4.24 MiB on loopback. The exact repeat used
7 KiB, an 8 KiB insertion used 96 KiB, and unrelated 4 MiB data used 4.02 MiB.
OpenXet retained 14.75 MiB after 32.02 MiB of logical commits. ZeroS3
retained 14.02 MiB on the same trace and sent the full body on exact repeat.
Seven of eight committed files reconstructed and passed SHA-256. One tensor
revision triggered a client range-bounds error on three independent runs,
including a run with the Xet chunk cache disabled.
Hugging Face bucket uploads took a median 5.32 s over nine cases. The
same-key rewrite used 25 KiB of TX. Unrelated 4 MiB writes used 4.05 MiB each.
Eighteen bucket downloads passed SHA-256. The dataset reused the same inputs
after the bucket. All eight commits showed Xet metadata and all 24 downloads verified.
Median dataset downloads took 5.15 s with a fresh Hub cache, 5.6 ms
from the local file cache and 3.81 s with a forced fetch. The Xet shard
and chunk caches were shared across the hosted sequence. WAN counters
include protocol overhead. Hosted physical CAS allocation is unmeasured.
[Public bucket](https://huggingface.co/buckets/insightlabs38-pixel/storage_test) ·
[Public dataset](https://huggingface.co/datasets/insightlabs38-pixel/storage_test)

## Commercial and engineering implications

Versioned build and package artifacts are the strongest S3 use-case hypothesis
in this sample. Offline archive is potentially attractive where long retention
pays for optimization and sparse reads are infrequent. Existing snapshot
tools narrow the value proposition for dedicated backup buyers.
C2 priorities are cheaper authority candidate admission, optimization
accounting and verified read/recovery costs. Defer blanket inline
representations and additional codec lanes until real histories justify them.
Availability, multitenancy, migration and observability need separate
qualification. [Cost sensitivity and adoption gaps](COMMERCIAL.md)
