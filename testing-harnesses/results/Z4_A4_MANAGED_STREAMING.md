# Z4 A4 managed streaming — active, unfinished

Status: first checkpoint; no production integration or readiness verdict.
Branch: z4/a4-managed-streaming. Production remains unchanged, managed creation disabled.

The first change replaces research PrefixIndex collision buckets and per-reference scans
with sorted original-index arrays and precomputed unique-prefix widths. Put is constant
work (at most 32 copied bytes); Get narrows sorted intervals without candidate allocation.
ClassIndex retains flat CSR members, class boundaries and sorted handle records;
Put is O(log N), including one class shared by every handle. Class numbering and member
ordinals preserve original occurrence order. Constructor sorting is O(N log N).
Legacy repeated-handle behavior (last class, first occurrence within that class) is retained.

The old implementation was mechanically extracted unchanged into legacy_a4_test.go.
Differential tests compare bytes, errors, trailing bytes and truncated/malformed streams
across 0/1/2/32/257/4096 entries, four prefix widths, deep collisions, ambiguous identities,
duplicate classes and repeated handles. Existing DEX/CLR tests remain intact.
Benchmark (100 ms, one run; indicative, not a production throughput claim):

| Class members | Legacy Put ns/op | Compact Put ns/op |
|---:|---:|---:|
| 1024 | 258.4 | 26.01 |
| 4096 | 1163 | 37.95 |
| 16384 | 3466 | 49.25 |
| 65536 | 9192 | 49.90 |

Each benchmark cycles handles; adaptive iteration counts differ, so the legacy largest
case samples only part of its first cycle. Both paths allocate zero bytes per Put.
This does not satisfy the complete A4 architecture gate. Whole-file input, masked copies,
reference accumulation, component retention and decoder proof remain to be replaced or
bounded; four-size real artifact memory evidence is still required. No completed verdict.

Runtime recovered after the earlier transport interruption. Fetch, toolchain and race
smoke tests succeeded; no OOM kills recorded. 3.8 GiB free on a 32 GiB filesystem at resume.
If disk becomes critical, remove only reproducible scratch first; preserve important
non-reproducible evidence on a separate unmerged backup branch if a stop is unavoidable.

## Second checkpoint: actual streaming/staged prototypes, still unfinished

DEX WalkAt reads 64 KiB source windows, with checked bounded instruction buffers.
An unchanged pre-streaming walker was mechanically extracted as an error/slot oracle.
Visit passes retain compact item descriptors and exact masks instead of all Slot records.
EncodeAt reads original identity fields in windows, hashes masked items without a file
copy, uses sorted offset lookups/bitsets, spools reference bytes, and emits components.
The masked view and windowed grouping preserve all legacy cuts and component bytes.
Component construction is capped at 16 MiB including reference streams; staged references
are capped at 256 MiB. Partial sink output is never a proof or permission to publish.

Real F-Droid 2.0.2 DEX: 9,297,608 bytes, 2,109 components. Fresh streamed encode
1.032 s, max RSS 88,801,280 bytes, sampled peak Go heap 78,805,832 bytes.
Metadata SHA256 1f115a254f969ab7349cee477b7a8703b4b5afbb2db6a979e9c531cec3b31969;
length-framed component SHA256 d1bcc4a78f83b9fd06aed07f151f3a26fd27284e3b84880a6de305181bdbdbca.
Both equal the legacy API output. Independent serialized full proof: 0.684 s,
140,603,392-byte max RSS; exact original SHA256 reproduced. Do not compare streamed
encode alone against legacy Encode's 1.742 s /347,590,656 RSS including its full proof.
The initial streamed prototype peaked at 163,323,904 RSS: retained slots/maps were
real remaining costs, subsequently reduced. All raw intermediate rows are retained.

CLR EncodeTo removes masked/payload copies and component/reference-byte retention,
with compact sorted temporary reference records. Candidate indexes are resolved one set
at a time, rather than retaining every overlapping coded/token candidate set.
This intermediate CLR API still takes a whole original image and retains slot/body state.
CoreLib 12,775,936 bytes emits 2,949 components; unchanged metadata/component hashes
and independent full SHA proof pass. Latest plain sample: 1.715 s /183,574,528 RSS.
Earlier sample was 230,649,856 RSS. This remains a gate failure, not readiness.
Profiles identify growing table/IL slot arrays, repeated candidate-input arrays and body
state as further work. Four-size memory evidence and production capability/wire/crash
checks are not yet complete; production source and managed defaults remain unchanged.

Validation: entire z3 module suite passed before final candidate-cache eviction refinement;
that refinement passes exact CLR fixture comparisons plus additional race checks.
DEX/CLR fixture metadata, all component bytes, cuts, Stats, and independent decodes
match legacy across prefix/cutter settings. Original tests were retained; the noncanonical
DEX Auto+K0!=3 combination already fails legacy self-proof and the streamed API refuses
it explicitly. Window geometry, source errors, spool budgets/order and cleanup are tested.
Full logs, negative samples and source hashes: a4-checkpoint2-evidence.json.gz.

## Third checkpoint: CLR source windows and four real sizes

EncodeAt now owns bounded metadata heaps and reads method records through ReaderAt;
no full source image, full masked image, payload copy, or output list is retained.
Compact sizing passes allocate slots once; method tokens/clauses are released after
slot discovery in favor of small body geometry. Overlapping candidate sets are evicted.
Reference sorting now uses 4096-record external runs (up to 512 merge cursors, 4 KiB
each) and one bounded record buffer, replacing the full-reference sorting index.
Fresh-process plain controls, OS cache warm; producers/compilers outside measurement:

| Real CLR input | Bytes | Encode s | Process max RSS MiB |
|---|---:|---:|---:|
| Newtonsoft 13.0.4 | 717736 | .235 | 22.16 |
| FSharp.Core | 5726208 | .676 | 37.72 |
| CoreLib 8.0.33 | 12775936 | 1.617 | 78.48 |
| Roslyn (SDK 8.0.427) | 18601984 | 2.249 | 104.78 |

All four preserve exact metadata and every length-framed component byte versus legacy;
independent serialized full SHA proofs pass. Legacy encode includes self-proof; its
Roslyn RSS 409.56 MiB is not an encode-only timing comparison. Prior negative samples
remain in evidence. Controls are single fresh-process samples, not statistical medians.
Creation remains bounded to 32 MiB source, metadata heaps 32 MiB aggregate /16 MiB each,
1M aggregate identity rows, 256K heap entries and 64 MiB estimated context metadata.
Method code is capped at 1 MiB, record windows 16 MiB, cumulative method inspection
16*source+64 MiB and 1M token/clause records; shared bodies are parsed once.
Canonical identity bytes are charged (including repeated long suffix names), and
identity dependency depth is bounded at 256. DEX prototypes hash parameter descriptions
incrementally under an aggregate byte budget, avoiding identity-slice expansion.
Adversarial tests cover shared bodies, repeated long suffixes, shared large prototypes,
identity depth, metadata count limits, source I/O failure and external merge ordering.
The full module suite, focused races and vet pass; the unchanged deep prefix oracle
also passed under race (485.890 s). Latest budget/stream refinements pass focused tests.
Mechanical AST extraction builds a standalone stdlib-only managed port; actual production
integration, five frozen root vectors, old/new wire/capability/crash gates and final
native production measurements are still pending. PR #95 stays draft; no managed merge.

Decoder closure checkpoint: CLR borrows its reconstruction heaps under the same metadata
bounds, tiles payload directly without a second full concatenation, and caches bounded
method-body parses. DEX preallocates exact slot capacity, uses compact offset state and
streams budgeted prototype identities. Native Roslyn and DEX independent proofs still pass.
Codec/kinds suites, focused races and vet pass; an initial wrong package-path invocation
is retained alongside its successful corrected rerun. Evidence: a4-checkpoint4-evidence.json.gz.
Production source remains unchanged; all production merge gates are still pending.
