# Z3-44 broad tail

Verdict: **DEFER** ONNX integration; retain typed research adapter. WOFF2/7z
framing **KILL** on measured gaps; Avro/WAL **DEFER**. Production untouched.
Only ONNX was deeply implemented after the five-format shootout.

Selection weighed observed revisions, CDC gap, exact recovery, existing primitives,
component identity, implementation cost and useful ranges. ONNX's named typed
weights and initializer-order gap outrank WAL's inconsistent page boundary result.
Fonts provide natural release artifacts, but codec replay cost is disproportionate
to their small corpus; Avro/7z lack a stronger exact component mechanism here.
These are qualitative evidence ranks, not market-prevalence estimates.

44 inputs / 41 unique SHA256s: 12 committed SQLite WAL captures from pinned public
Chinook (five append, four checkpoint, three independent); Font Awesome 5.15.3,
5.15.4, 6.0.0 plus two same-input Brotli quality reexports; twelve Avro producer
states (null/deflate × base/cell/append/metadata/repack/field-order); six native
SqueezeNet-1.0/opset-12 model states; nine native 7z archives of real hiredis
v1.2.0–v1.4.0 sources (Copy, solid/non-solid LZMA2, uncompressed headers).
Only fonts are downloaded release histories. WAL/Avro are producer workloads;
ONNX is a real public seed plus controlled edits; 7z is real release-source
archiving, not published archive history. No natural model/WAL history claimed.

Public URLs/commits/hashes, producer wheel URLs/hashes, versions and scripts are
in analysis `testdata/q16-tail`. Tools: SQLite 3.53.1, fontTools 4.61.1,
Brotli 1.1.0, fastavro 1.12.0, ONNX 1.19.1, protobuf 6.33.0, 7zip 26.01.
Native oracle gates: each WAL/base pair reopens with integrity/query-SHA parity;
font inverse transforms rebuild usable SFNT; Avro rows compare exactly; ONNX
checker passes; 7z tests and every extracted source file compare exactly.
Producers/oracles run separately; no external executable at candidate decode.

Packed history bytes include roots, reachable closure and 64 B/object:

| Shootout cohort | Source CDC | Native-boundary oracle | Decision |
|---|---:|---:|---|
| WAL, three workload families | 837,972 | 1,042,957 | DEFER |
| Three font releases | 284,318 | 284,212 | KILL framing |
| Avro, two codec profiles | 4,385,314 | 4,138,799 | DEFER |
| ONNX, six states | 6,569,156 | 4,977,942 | Deep target |
| 7z, three codec profiles | 788,951 | 781,818 | KILL framing |

WAL pages help independent replays (354,503→268,463 B), but checkpoint generations
lose (479,295→769,702 B). Append-only lineage already costs just 4,174 B total.
Avro fine CDC is 3,652,505 B, better than native block grouping. Logical-only
oracles omit framing/schema/coding choices and never claim exact outer recovery.
WOFF2 quality reexports: source 160,321 B vs logical transformed tables 87,764 B,
but opaque compressed boundaries still cost 160,319 B; exact Brotli decisions
remain missing. 7z solid compressed history beats its plaintext-file oracle.

Deep ONNX: bounded typed Model.graph/Graph.initializer/Tensor framing, not generic
protobuf normalization. Dense raw tensors validate name/type/dimensions/raw length;
unknown fields, sparse/nested attribute tensors and typed scalar payloads stay
opaque. No graph rewriting, value normalization, external path traversal or BASE.
Six-model native parity covers 52 raw initializers each, every name/dtype/shape/
payload SHA256; the 53rd initializer is a typed scalar preserved as opaque bytes.
Five original native fixtures cover reordered/raw/typed/external/unknown profiles.
External fixture sidecar validates at producer time; graph-byte tests omit it.
No full external model-set storage or external-weight reconstruction claim is made.

The 293-line adapter reuses Q12's checked region composition/PLANES and Q13/Q14's
mechanically copied accounting/range harness. Limits: source 128 MiB, tensor/region
16 MiB, one million fields, 65,536 initializers, rank 32, name 4 KiB; lengths and
shape products check before use. Unsupported wire groups/ambiguous duplicates
reject. Unknown fields are exact bytes, not interpreted. Decoder is existing
Concat/Stored, optionally existing analysis Model planes; no new persistent kind.
Future production Model use still needs the unmerged I2 draft approval.

ONNX raw/fine/planes histories: 4,978,293 / 5,241,439 / 4,811,061 B. Planes save
26.8% standalone versus CDC, but CDC + planes is 11,380,217 B (+73.2%).
First sight: CDC 4,954,885 / raw 4,955,583 / planes 4,781,439 B.
Strongest positive: initializer reorder adds 1,568,158 B under CDC, 5,770 B under
the oracle. Names are diagnostics only; admission/reconstruction identities are
SHA256. Strongest negative: every additive layout retains more bytes than CDC.

214 representation rows: 150 full outer proofs plus component proofs = 1,866
serialized exact proofs; 450 fresh 4 KiB ranges, all reads in declared closures.
ONNX max logical Source API amplification: CDC 21.84x, raw 36.89x, fine 14.06x,
planes 5.68x; not physical CAS I/O. Full SHA256 proof is separate from partial reads.
Final harness 2.668 s / 77,204 KiB RSS (75.4 MiB), compiler/producers excluded;
includes whole inputs, accounting/proofs/ranges, not a streaming production port.

Complete analysis tests/vet pass. Malformed lengths/varints/shapes/duplicates,
missing external files, unknown-field preservation and source/overlap negatives
are covered. Fixed-count fuzz: 2,000 mutations / 0.121 s PASS; the earlier timed
run passed with only 27 mutations, so it is not presented as broad coverage.
No concurrency/production change requires broad race or blackbox reruns.

Promotion needs related natural model histories and layout/lifecycle economics
that beat retained CDC. Carry forward typed tensor framing and bounded range reuse;
keep the other four formats as characterized negatives/deferred lanes.
