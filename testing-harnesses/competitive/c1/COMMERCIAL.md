# Commercial assessment

## Illustrative economics

Model prices at $12–$30 per retained decimal TB-month and $0.04–$0.12
per vCPU-hour. These are scenario inputs. They exclude redundancy,
migration, operations, retrieval and egress. Assume like-for-like storage
durability for the arithmetic. The VM measurements do not establish that
all tested configurations actually provide it.

The three-revision composite contains 89.80 MiB of logical objects.
Archive authority optimization reduced ZeroS3 from 40.35 to 35.57 MiB.
That is 4.78 MiB saved, or about 0.053 TB retained less per logical TB.
The implied monthly storage saving is $0.64–$1.60 per logical TB.
The measured median 12.63 CPU seconds for optimization and GC would
correspond to about 37 CPU hours per logical TB if input size scaled
linearly. Under that assumption it would cost $1.49–$4.47 once, with a
storage-only payback around 0.9–7 months. The compute estimate excludes
new ingest or repeats triggered by new revisions. The study did not
establish linear scaling. Archive also increases observed whole-object
read work and peak temporary allocation. Frequently read data needs its
own retrieval-cost and service-level budget.

Default ZeroS3 allocated 40.35 MiB for this workload. The same versions
occupied around 83–96 MiB on the two measured full-object servers
SeaweedFS and RustFS. At a fixed price and redundancy level, that gap has
more headroom than archive versus default. It depends on the overlap and
compressibility in this fixture, which include exact copies. Borg and
Restic were already in the same storage range as ZeroS3, so storage
savings alone offer a thinner reason for a snapshot buyer to migrate.

## Segments and gaps

- **Versioned artifacts and build outputs.** A credible use-case
  hypothesis is a self-hosted S3 repository with immutable build and
  package versions. The defensible benefit is the combination of signed
  object operations and reduced allocation on overlapping revisions.
  Measure customer artifact overlap, read mix and migration effort before
  making volume or ROI claims.
- **Cold archives.** Archive authority merits further work for
  long-retention, rarely restored data. Per-family hard-codec savings,
  verified recovery and the effect of planned migration or optimization
  on read service determine suitability.
- **ML checkpoints and dataset histories.** Related data may compress or
  deduplicate locally. Compare on real checkpoint families and Xet's
  measured WAN transfer path. Retained capacity, WAN transfer and client
  reconstruction are distinct cost items.
- **Backup and database snapshots.** Mature backup tooling already reduces
  physical footprint and includes snapshot retention. S3 compatibility is
  the prospective differentiator for workload owners who need object
  access. C1 provides insufficient Parquet and SST data for a claim.
- **General S3 replacement.** The loopback trials support further
  evaluation on one node. Availability, recoverable backup, multitenant
  isolation, client compatibility, ACL/IAM breadth, observability, data
  migration, supported platforms and license/deployment review are
  separate adoption requirements with unpriced operational costs.

C2 should measure authority candidate discovery and staging costs and
identify when the optimizer can cheaply skip weak candidates. Verify
range-read and end-to-end recovery costs before raising archive
admissions. Defer blanket inline representations and new codec families
until per-family evidence balances the retained savings against CPU,
temporary disk and reconstruction work. A multitenant or distributed
launch requires separate architecture and failure testing.
