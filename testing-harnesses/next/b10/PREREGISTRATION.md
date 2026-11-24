# B10 pre-registration (written before any comparative store measurement)

Base: `d4ba342` (kind 16 implemented; only engine-independent oracles and single-file probes run so far:
gems deep/raw delta proxy 0.18-0.22, Maven 0.72 with later point releases worse, OCI python layers ~0.95).

## Families (published or producer-generated; digests in corpus.sha256, derivation in make_inputs.py)
| family | inputs | structure | question |
|---|---|---|---|
| gems-rubocop | rubocop 1.66.0, 1.66.1, 1.67.0, 1.68.0, 1.69.0 `.gem` | tar ⊃ gzip ⊃ tar | nested sharing B8 cannot see (outer tar is raw) |
| gems-ar | activerecord 7.2.0, 7.2.1, 7.2.2, 8.0.0 `.gem` | tar ⊃ gzip ⊃ tar | second nested family |
| maven | apache-maven 3.9.6-3.9.9 `-bin.tar.gz` and `-bin.zip` | gzip ⊃ tar ⊃ ZIP (jars) | three layers; jars mostly byte-identical across releases |
| oci-python | python:3.12.{6,7,8}-slim amd64 layer 2 blobs (Docker Hub) | gzip ⊃ tar ⊃ ZIP (pip wheel) | real OCI layers, one nested unit |
| pq-snappy, pq-zstd, pq-lz4 | OWID co2 CSV at 4 commits → pyarrow 18.1.0 `write_table` | Parquet pages | compressed structural units |
| sst-snappy, sst-lz4 | same rows keyed country\|year → rocksdict 0.3.29 SstFileWriter | RocksDB data blocks | second structural family |
| ts | typescript 5.6.2, 5.6.3, 5.7.2 `.tgz` | gzip ⊃ tar, no nested unit | B8 canonical baseline: splice must not regress |
| ctl-rnd | 3 tars of gzip(random) and fake gzip members | malformed/incompressible | must stay at CDC |
| ctl-unrelated | rake, rack, thor, puma, nokogiri (one version each) | low overlap | no false sharing |
| ctl-trunc | truncated Parquet + valid v3 | malformed | refused safely |

## Lanes (archive profile unless named; order oldest, depth 2, 2 bases; records grouped for b9*/b10*)
cdc; **b9** (B8 canonical + grouped records); **b9x** (b9 + existing codec replay + structured kinds 9/10 + hard
codecs: the strongest existing baseline); **b10** (b9x + kind 16); ablations b10-nonest (no nested children),
b10-nopage (no pages/blocks), b10-nodelta (deep streams only as CDC); b9x-mixed, b10-mixed, b10-hot.

## Measurements
Allocated bytes after optimize + gc + repack + compact, deep verify, plan = materialized model; cold (page cache
dropped) 4 KiB / 64 KiB / 1 MiB / whole reads and warm repeats; 8-reader concurrent throughput, p95/p99; optimize
wall/CPU/RSS and evaluations; every read compared byte for byte.

## Criteria (fixed now)
* Nested success: b10 ≤ 0.80 × b9x allocated on both gem families.
* Structural success: b10 ≤ 0.80 × b9x on at least one Parquet and one SST family (Snappy or LZ4).
* No regression: b10 ≤ 1.01 × b9x on ts, maven, oci-python and every control; zstd pages may be refused.
* Read pricing: b10-mixed never selects a form whose modeled cold 4 KiB read exceeds the profile budget; hot
  stores no kind-16 record. Measured cold 4 KiB of b10 archive stores is reported against b9x, with no bound
  claimed: a spliced form above 2× b9x is labelled archive tier.
* Verdict per the B10 brief: EFFECTIVE needs nested and structural success on ≥ 2 independent real families with
  justified read cost; SPECIALIZED if only one class succeeds.
