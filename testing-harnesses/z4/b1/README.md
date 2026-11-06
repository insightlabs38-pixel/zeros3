# B1 reproduction
Research only. No production storage format, authority or scheduled routine.
Use Go 1.27, Python 3.12 and Git; all Go packages are stdlib/internal only.

```sh
python testing-harnesses/z4/b1/prepare.py
go -C testing-harnesses/z3 build -o /tmp/b1 ./cmd/z4similarity
python testing-harnesses/z4/b1/run.py --binary /tmp/b1
mkdir -p /workspace/work/z4-b1/struct
/tmp/b1 -export-struct -manifest /workspace/work/z4-b1/manifest.json -out /workspace/work/z4-b1/struct > /workspace/work/z4-b1/structural.jsonl
python testing-harnesses/z4/b1/run.py --binary /tmp/b1 --mode components --manifest /workspace/work/z4-b1/struct/struct-manifest.json
```
`prepare.py --work DIR` reuses the pinned Q4/Q6/Q8/Q15 caches and existing I1C
hiredis repacks. Only cheap Git subset exports and explicitly synthetic controls
are generated; no compiler/model/database producer is rebuilt. Inventory hashes
must match the committed evidence. Git pins and producer version are recorded.
Cache locations/provenance are in `prepare.py`, the inventory and prior reports.

Additional measured modes (use `measure.py COMMAND ...` for one isolated child,
stdout JSONL, stderr sampled Go heap plus OS RSS/CPU/status):

- `-quality`: 16 target chunks x up to64 candidate bases, exhaustive within that
  sampled subset, four candidates per scheme; useful means >10% COPY/ADD gain.
- `-baseline-ranges`: compressed CDC/fine-CDC payload amplification, same requests.
- `-scale N`: sorted ReaderAt table at100k/1M/10M chunks, four postings/chunk;
  `-hash-index` compares append/delete/rebuild at100k/1M. Real temp writes/sync,
  binary searches, short-write checks and cleanup; queries have warm page cache.
- `-depth 1|2|4`: use separate `-out` directories to compare dependency policies.
- `-mode fixed64 -cold`: numeric LSH and PXR, including predictor-without-base.
  `-numeric` separately compares aligned XOR/plane/float/PXR oracles.
- `-bench CODEC`: 20 aligned64KiB pairs, separate encode/decode timings, no index.
- `-replay STORE`: reopen only serialized roots/deltas, authenticate dependencies
  and native source hashes; component exports also replay their native format roots.
- `-aggregate STORE`: dependency-first1/16/64MiB layouts, actual aggregate payload
  construction/extraction and outer compression/replay; overheads remain modeled.

Each default discovery scheme gets its own capped posting index. All four active
indexes are charged; numeric/coarse/sign indexes are additionally charged in cold
mode. Sorted scale experiments use four postings, **not** the entire six-scheme
ensemble: multiply disk/resident costs by actual posting count. On-disk sorted
levels permit prefix sharding; authenticated levels, LSM compaction, cold-device
latency and a cache service are future prototype work, not implemented claims.

Economics: production CDC16/64/256KiB, fine CDC2/8/32KiB (8x metadata sensitivity),
existing pack compression rule, 96B record+locator, 36B/root reference. All exact
experimental envelope bytes, component wrappers, indexes and historical bases
are charged. Common pack header/footer80B excluded outside aggregation. Both
per-revision cumulative and final history totals are recorded. Additive storage
retains authoritative CDC **plus** the independent complete delta closure; no
production disk saving is claimed. Migration temporarily costs that same sum.

Study time includes baselines, discovery, eight candidate trials, codec comparison
and serialization. Codec totals condition on the common historical store; they
are not independent retained-history policies. A missing candidate falls back to
direct; default-mode PXR/predict entries are direct placeholders, **not** executed
PXR results. Explicit codec benchmarks and cold mode exercise those algorithms.
Pdelta remains an estimated cost instrument and can never win admission.

Five-ms heap samples can miss short peaks; OS RSS is the high-water mark. Fresh
replay has no decoded cache; OS page cache remains warm. Range probes use start,
middle and tail positions; structural metrics address component streams, not
native-format application ranges. Whole-pack fetch/GC pinning is a layout model,
not a deployed remote-storage protocol or a production latency measurement.

`collect.py` checks serialized closure counts, wire-byte sums, exact fanout/depth,
per-stage totals and additive accounting, then writes deterministic evidence and
a pinned real CLR replay fixture. It preserves failed diagnostics when present.
Native Git independently checks the new Git delta program in the Go tests.
Export publication stages a clean directory and preserves the preceding export;
the original stale-fixture accounting failure and repair remain in evidence.
