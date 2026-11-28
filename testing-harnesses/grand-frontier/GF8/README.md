# GF8 · integrated qualification

GF2 bit patch and GF7 corrected token translation are the opt-in native
candidates. CDC and the current canonical archive remain the production
comparators. GF6's receiver-aware coordinator stays in research.
The planner selects from bounded materialized candidates and requires
read service measurements. Existing ingest, J2 and native FD paths
retain their current semantics.

## Signed mixed requests

Two workers each issued 96 signed requests in two opposite lane orders.
Every lane and round checked 120 4 KiB requests, 24 whole GETs, 24 HEADs
and 24 PUTs. PUT payloads were read back after acknowledgement. The
reader kind was checked after reopen and before the first request.
Full allocation includes both revisions and finalized metadata.
Additional hot-overlay footprint was zero. Timing records after reopen
retain the underlying OS page cache.

Each percentile is descriptive. With 120 range samples, nearest-rank
p99 is the 119th value. For the other 24-sample operation types the
rank is the maximum. Two rounds reveal order sensitivity and cannot
establish a fleet service objective.

| Lane | Total bytes | 4 KiB p99, ms | Whole p99, ms | HEAD / PUT p99, ms | Prep CPU, s | Request CPU, s |
| --- | ---: | ---: | ---: | --- | ---: | ---: |
| CDC | 8,392,704 | 1.66–2.89 | 11.45–13.52 | 0.68–0.84 / 3.29–4.10 | 0 | 0.134–0.141 |
| Bit patch | 5,414,912 | 2.60–4.47 | 39.11–118.17 | 1.94–2.42 / 1.91–2.39 | 1.59–1.72 | 0.68–1.18 |
| Token translation | 4,333,568 | 6.38–9.59 | 228.30–340.85 | 0.65–0.72 / 3.50–5.60 | 1.19–1.28 | 4.71–4.74 |
| Archive | 4,829,184 | 89.52–100.39 | 2,277–2,324 | 54.88–61.09 / 65.83–71.69 | 26.41–26.78 | 52.75–53.02 |

The table reports one write-accounted run. Two further runs capture
stable sizes and tail variation. Request CPU includes PUT readback. Logical
ingest wrote 8.37 MB per store. After ingest, Linux write calls across
preparation and finalization totaled 8.37 MB CDC, 6.57 MB bit patch,
6.71 MB translation and 162.48 MB archive. Final repack writes alone
were 8.37, 4.19, 4.19 and 0.21 MB respectively. The aggregate count
includes staging and metadata and excludes later signed requests.
Kernel-charged write bytes appear separately. These counters measure
process I/O and cannot establish device write amplification.

[Accounted mixed rows](results/mixed.jsonl) ·
[First repeat](results/mixed-repeat.jsonl) ·
[Final wrapper repeat](results/mixed-final-replay.jsonl) ·
[Validation receipts](results/validation.jsonl)

## Recovery and compatibility

| Boundary | Exercised path |
| --- | --- |
| Interrupted and repeated publication | Both compiled formats with process cuts after CAS flush, format fence and publication. Verify, reopen, retry and compact. |
| Retention while publishing | Exact snapshot protects a retired anchor through prune and compaction before dependent publication. Drop the snapshot, compact again and verify both compiled formats. |
| Missing shared basis or frame | Verify catches each missing component. Object-scoped signed closure recovery supplies it, followed by reopen and exact reconstruction. Exercise both kinds. |
| Corrupt payloads, cycles and cancellation | Native authority rejects corrupt components and cycles. Malformed bit-patch layout and oversized stored members are refused. Oversized root loading also has a measured allocation bound. Bounded program and index fuzz cover both experimental decoders. Authority migration cancellation preserves the old committed state. |
| Physical movement and FD lifecycle | Compiled frame packs move tiers before exact reads and repair. Native FD tests cover a three-descriptor budget, seal, retirement and concurrent reads. |
| Format compatibility and rollback | Actual older executable, feature-negotiation refusal, new native import, ordinary snapshot bundle import, exact downgrade and old-reader recheck for bit patch. |
| Journal and reconciliation | Grouped J2 replay, failed group sync, format fence, corruption and ahead-of-journal checkpoint tests. Signed current projection sync and exact GF6 retry. |

Generic repair consults the ordinary manifest and may fail to discover a
retired CDC leaf or locate a missing representation member. The verified
recovery in this experiment uses the object's signed native closure.
See [bounded replay](REPRODUCE.md) for device and resource exclusions.

## Decision

The new readers target different operating points. GF2's compact bit
copy favors whole restores, and GF7 uses less space when token overlap
survives table drift. Include ordinary CDC for urgent sparse or whole
work, and keep archive as a control when retention changes the minimum
space lane. Illustrative paired caps of 3 ms sparse and 15 ms whole
select CDC in the accounted replay. Budgets of 5 ms and 125 ms admit
bit patch. Budgets of 10 ms and 350 ms admit translation. Repeats include
a 17.86 ms CDC whole p99 and a 6.80 ms bit-patch sparse p99. They
explain why GF1's work cap requires service calibration and margin.

For one-time migration economics, amortize prep CPU and written bytes
against the retained-byte difference and the anticipated number of
mixed cohorts. Include the incremental request CPU per cohort. The
translation lane saves 4.06 MB versus CDC and consumes about 4.6
additional CPU-seconds each observed 192-request cohort. The absolute
economic break-even depends on capacity price, device costs and retention
time. This replay starts from ordinary ingest and does not measure a
live fleet migration.

The [adjacent held-out](../GF7/README.md) signed result uses a separately
built store. The [native transfer results](../GF6/README.md) are local
rate and delay shaped, with exact body and request accounting. The
[technical report](../RESEARCH_REPORT.md) describes scope and the
[verdict](../FINAL_VERDICT.md) records the tested and open conditions.
