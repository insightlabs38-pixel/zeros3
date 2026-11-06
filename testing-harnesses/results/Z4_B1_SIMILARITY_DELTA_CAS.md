# Z4-B1 — Generic similarity/delta CAS frontier
Verdict: **SIMILARITY_LAYER_READY_FOR_PROTOTYPE**, research qualification only.
[PR #96](https://github.com/insightlabs38-pixel/zeros3/pull/96), branch `z4/similarity-delta-cas`; checkpoints `313cab0`, `5ff0e91` plus final qualification commit in that PR. GitHub records integration status.
Starting main `036ac2ca8096fac311754b2e74b6ba143861219d`; A4 #95 merged; #80/#81 preserved. Production source SHA256 remains `ac29a5646dd32a7c388a7325aa8929f3d5087cf3d362fee205d63b74f6b6f493`.
No production authority, wire format, version, defaults, lifecycle or S3 behavior changed. No B2 implementation or automation.
Evidence: [raw results, inventories, timings and diagnostics](../z4/b1/evidence.json.gz), [reproduction](../z4/b1/README.md), [pinned real CLR replay](../z4/b1/clr-replay.json.gz).

Design: SHA256 target identity plus exact direct/residual/COPY-ADD envelopes; every used base carries SHA256 and length. Decoder verifies every base and final output. Default depth2, hard4, fanout4; graph cycles, missing/corrupt bases, malformed lengths and cumulative expansion budgets fail closed.
Codec inputs <=16MiB each; <=4 bases; compact anchor index with approximately262k sampled anchors with adaptive sampling stride, eight collision probes and four positions/hash. Work is bounded; lossy hashes only propose candidates, never establish correctness.
Discovery: rolling bottom16 windows, four superfeature bands, prefix/suffix, local4KiB features; cold mode adds coarse numeric features and eight positional-sign LSH bands. Query postings retain newest64 IDs, deterministic ranking, at most eight candidate trials. Hot-feature eviction intentionally trades recall for bounded work.
Best general discovery is bottom16 plus local features, with cheap edge/band filters. Sampled COPY/ADD oracle: local68/69 useful queries recovered, bottom65/69, coarse65/69, edges52/69, super31/69, sign22/69. Useful-candidate precision respectively94/158,88/139,85/128,73/98,42/42,30/297. These are top4 results over <=64 sampled bases and <=16 targets/family, not full-store oracle recall.
Compare actual serialized XOR+DEFLATE, sparse XOR, changed ranges, rolling24/stride8, independently encoded Git COPY/ADD, adaptive8/stride4/four-location matching, existing byte-plane residuals and PXR. Existing pdelta is a cost estimator only, never an admitted representation. No external ANN dependency or copied GPL implementation.
Adaptive matching handles displaced boundaries and genuinely multiple bases. Git's compact op format frequently wins single-base comparisons; sparse residuals win aligned/local edits. All candidates and two/four-base programs compete against direct storage. Existing DEFLATE handles literal runs; PXR supplies the stronger non-DEFLATE adaptive residual/control.

Costs below are cumulative retained histories, normalized to production CDC. Fine=2/8/32KiB versus production16/64/256KiB. Generic includes all experimental envelopes, roots, bases and four active discovery indexes. Format uses existing exact managed4096/Auto12, SST UKV8192, Parquet8192, Model planes, Git and tar/Zstd adapters; it is the prior qualified reference, not a new exhaustive format-parameter sweep.
Hybrid applies generic deltas to every reachable serialized format component, recipe and index, then independently replays native source hashes. It charges four-byte component framing and36B references; tiny-component overhead can outweigh reuse. Structural range metrics concern component streams, not native application ranges.

|Family/evidence|CDC MiB|Fine/CDC|Generic/CDC|Format/CDC|Hybrid/CDC|
|---|---:|---:|---:|---:|---:|
|Newtonsoft, five real releases|1.328|0.998|0.839|0.898|0.655|
|F-Droid DEX, three real releases|11.163|1.008|0.778|0.487|0.531|
|Pythia14M, three real training checkpoints|80.643|1.014|1.008|0.997|0.999|
|hiredis bitcode, five release-source compilations|0.151|1.031|0.660|—|—|
|ELF, three controlled producer revisions|0.038|0.885|0.414|—|—|
|PE, three controlled producer revisions|0.003|1.000|0.890|—|—|
|Parquet, four cached producer states|64.698|0.775|0.499|0.403|0.417|
|RocksDB SST, four producer states|0.816|0.937|0.711|0.657|0.544|
|Pebble SST, four producer states|0.783|0.986|0.739|0.697|0.579|
|Git, three pinned real commit subsets|2.590|0.804|0.609|1.048|1.437|
|Git, three real-history repack/re-encoding exports|19.020|1.004|0.641|0.648|0.704|
|OCI, two real-release Zstd reexports|4.166|1.014|0.997|1.136|1.018|
|coreutils tar, three real releases|42.919|1.003|0.546|1.088|0.739|
|Binary controlled edits/branches/entropy/misleading edges|1.299|0.806|0.587|—|—|
|Wasm custom-section control only|<0.001|1.000|1.500|—|—|
|GGUF first-sight tiny pinned fixture only|<0.001|1.000|1.967|—|—|

There are61 pinned inputs /479674741 source bytes. SST entries are retained files from four states, not whole database snapshots. Bitcode is compiled real source, not shipped release binaries. Git subsets export pinned historical commits; repacks are not independent natural revisions. OCI rows are reexports, not original registry gzip bytes. Real Wasm/GGUF histories remain unverified; their tiny controls support no broad negative conclusion.
Generic similarity replaces no required parser: it is particularly useful for raw/source/Git histories, complements CLR and SST, and is inferior to existing DEX/Parquet structural references here. A hybrid must select the best whole plan, not append every representation.

Accounting: production pack compressor,96B record+locator and36B/root reference; sorted index12B/posting plus32B/object. Every active scheme is charged, including first sight and dependencies across all retained revisions. Stage arrays give per-object increments and cumulative totals. Common primary-manifest headers and benchmark JSON transport are excluded from both baselines; compact36B root references are modeled. Common pack80B excluded except aggregation; estimates are not a byte-exact deployed store format.
Let C=CDC, S=complete standalone experimental closure including roots, I=discovery indexes. Standalone=S; hypothetical authoritative=S+I; additive=C+S+I; temporary migration peaks at the additive total. For source archives: authoritative0.546C, additive/migration1.546C; DEX0.778C/1.778C; CLR0.839C/1.839C. **No production disk saving while authoritative CDC remains retained.**
No dictionary/reference is free. Existing bases remain physically charged once, even if only one late target pins them. Cold replay intentionally has no decoded cache; repeated edges count again. GC experiments mark transitive closure; direct flattening survives deletion of historical dependencies in tests.
True multi-base admission: CLR3, DEX34, Parquet113, source157, real Git repack150, Git history17 accepted programs use >1 base; selecting among eight candidates is separate. Actual header fanout/depth and full closure byte sums are independently checked.
Depth1/2/4 authoritative ratios: CLR0.839/0.839/0.732; DEX0.813/0.778/0.769. DEX4KiB amplification grows11.8/17.2/17.2; CLR14.5/15.4/20.4. Keep default2; deeper reuse is not universally worth its read/dependency cost.

Numeric discovery repair: raw substring sketches mostly miss Pythia. Positional-sign LSH finds candidates, but the no-base predictor control is decisive. Cold fixed64KiB total0.8683C versus predictor-only0.8708C: actual extra similarity benefit only0.0025C. Of1290 chunks,1121 choose no-base prediction and168 dependent PXR; most gain is compression, not historical reuse.
Cold study71.04s, sampled heap389.5MiB, RSS411.9MiB; separate cold graph decoding10.89s within the study, plus fresh exported replay in evidence. Treat PXR as an explicit archival optimization, not synchronous ingestion. The optimistic aligned oracle and ordinary XOR/plane/float alternatives are retained; do not generalize this small incremental delta gain to other model families.

|Four-posting index|Sorted build|Disk|Resident array|Warm ReaderAt query|
|---|---:|---:|---:|---:|
|100k chunks|83.8ms|4.8MB|6.4MB|4.86µs|
|1M chunks|1.020s|48MB|64MB|7.34µs|
|10M synthetic chunks|11.917s|480MB|640MB|11.58µs|
10M process RSS620.4MiB; sampled heap611.5MiB; real480MB temporary table written/synced/deleted. These are synthetic feature entries, not ten million payloads. Actual multi-scheme posting counts must scale the four-feature figures; no claim that the entire ensemble costs48B/chunk.
Hash100k/1M: build70ms/1.858s, query0.423/0.841µs, heap33.5/374.8MB; 1M RSS428.1MiB, deletion/rebuild612ms. Sorted arrays are substantially denser; ReaderAt supports bounded residency and prefix shards. Append-heavy immutable levels, authenticated indexes and cache/compaction policy need a prototype; no repeated full-corpus query scan or quadratic constructor is used.

Isolated20x64KiB F-Droid sampled pair codec throughput (MiB/s encode/decode): direct51.7/105.8, XOR55.7/75.5, rolling19.3/128.7, Git18.7/146.6, adaptive20.3/123.2, PXR2.8/9.4, predictor-only13.5/10.3. These omit discovery and are not aggregate ingest claims. Other codecs and CLR/model/archive samples are in raw evidence.
All-candidate study time / fresh-process whole-history replay / RSS: CLR2.17s/0.143s/40.4MiB; DEX12.19s/0.920s/139.0MiB; Parquet13.06s/2.425s/337.5MiB; source archives72.56s/8.094s/594.7MiB. Source sampled heap556.3MiB. Study time includes baselines, candidate trials and serialization, not only the admitted codec.
Five-ms heap sampling can miss short peaks; RSS is the process high-water mark. Final reported performance processes ran sequentially; no concurrent test workload is labeled isolated. Fresh replay removes decoded caches but does not evict OS page cache. Corpus payloads remain in-memory research state; the index experiment alone establishes scale, not a complete bounded-resident production store.
Cold4KiB payload amplification CDC/fine/generic: source3.2/0.7/5.3, DEX9.4/2.0/17.2, CLR9.2/1.6/15.4, Parquet11.0/1.7/16.9, Git-history14.6/3.1/22.3. DEX64KiB0.9/0.6/1.6 and1MiB0.5/0.5/0.7. These are beginning/middle/tail byte models, not device-latency measurements. Admission must charge read cost; small savings do not justify extra random reads.

Aggregation constructs dependency-first1/16/64MiB layouts, extracts and replays every indexed record, and verifies optional whole-pack DEFLATE. Production's existing64MiB pack target is the reference; records keep96B metadata/object plus80B/aggregate, so fewer physical objects do not erase locator costs.
At16/64MiB: source archives2/1 aggregates; model6/2; Parquet2/1. Model latest-only GC pins1.81x/2.99x live-record bytes. Whole-pack cold4KiB fetching amplifies6813x/16380x for model and5089x/8105x for Parquet. Independently addressable compressed records retain byte-range access and can coalesce dependencies; whole-pack compression is unattractive for interactive ranges. Warm storage/locality models do not prove a new remote protocol or transport win.

Validation on final source: complete analysis-module tests and vet; focused race tests including concurrent compressor reuse; bounded decoder fuzz293293 executions/10s;800 randomized codec round trips, maximum16MiB/four-base input, malformed/truncated/overflow/cycle/depth/work cases and short-writer regression. No canonical test removed or weakened.
Native Git independently consumes the newly encoded Git delta program, indexes strict packs and returns the exact target. Git corpus packs also pass native strict indexing. Pinned real CLR serialized replay freezes the original717736-byte SHA256. Every raw and structural export is replayed in a fresh process; native structural roots reconstruct exact original hashes.
Production CDC/Git smoke and root vet pass; root dependency checks remain stdlib-only. Production source is identical, so B1 intentionally does not repeat A4's full production/SIGKILL/old-binary lifecycle matrices. No production merge qualification or new persistent capability is claimed.
Found/repaired: repeated per-anchor heap allocations replaced with a compact bounded table; compressor state reused with byte-identical wire tests; repeated posting IDs no longer inflate similarity scores; no-base predictor control prevents false delta attribution; actual used-base depths are charged; clean staged exports preserve prior namespaces.
The strict accounting checker exposed21 stale tiny-fixture objects after switching Git corpora. They were preserved outside the qualified stores; the failed check and mapping remain in evidence. A directory-rename failure in the new regression was fixed without weakening it. All final closure/count/byte/stage checks pass; failed diagnostics remain durable.

Conclusive here: universal additive disk savings are impossible for this independent sidecar model; whole-pack cold range fetching is costly; tiny metadata/index premiums matter; pure substring discovery misses changing numeric patterns; most measured Pythia gain is no-base compression. No broad mechanism or format KILL/DEFER is issued.
Open: wider real Wasm/GGUF/model histories, stronger alignment-independent numeric ANN, advanced substring/oracle bounds beyond sampled pools, large-store level compaction/cache behavior, cold devices, sustained random reads, and range-aware admission. Sophisticated encoders remain justified where the oracle and workload show opportunity; these are research extensions, not concealed PASS gates.
B2 implication: prototype authority must explicitly route logical SHA to direct/delta encodings, authenticate/mark base closure, handle availability, crash-safe publication/rebase, bundles/replication, compatibility and rollback before CDC retirement. Preserve direct/fine-CDC/structural alternatives and read-cost admission. B2 remains separately allocated and DRAFT_ONLY.
B1 is complete as a qualified reusable research foundation with scoped open directions, not production integration. Stop here; no B2–B8 or routine was started.
