# Z4 A2 Model production

Verdict: **MODEL8_PRODUCTION_READY**. Production merge allowed after this checkpoint.
Base main 710248c; extraction source #80 43dfe419. Original #80/#81 stay drafts.
Only Model kind 8 is integrated; no managed parser/codec/index helper is included.
Production remains one stdlib-only source, default representation mode off.

## Implementation and mechanical reuse

Shell/Python extraction copied 1,418 parser/decoder lines and 296 bounded-analyzer
lines with source block hashes. Registrations add Safetensors/GGUF detection only.
Decoder operations preserve exact ordinary SHA-256 identity; names/dtypes/shapes
are candidate/index hints. Durable bases name SHA dependencies, never bucket/key.
Headers/gaps/tails use CDC. Tensor payloads use standalone byte planes or bounded
XOR-plane residuals; 256 KiB windows, at most three tensor hops / depth four.
No whole-model or whole-tensor allocation was introduced. LOC is not a veto.

Limits include 16 MiB metadata, 4096 tensors, Safetensors rank 32, GGUF 65536 KV
and 1 MiB strings, eight candidate bases, 65536 recipe operations, width 4096,
16 MiB residual chunk and cumulative 1 GiB input/base inspection. An exhausted
analysis budget or unsupported feature falls back to authoritative CDC.

A retained regression exposed aggregate replay missing from the draft: a small
zero-output DAG exceeded 600,000 source reads without an existing limit firing.
Shared atomic work accounting now bounds nested pipe readers to 1,048,576 steps,
64 MiB recipe/member metadata and intermediate bytes ≤16×root size+64 MiB.
Reservations cannot wrap. The same case returns errRepLimit at ~209,441 reads.
Range calls charge actual windows rather than whole referenced tensors; a
16 MiB tensor/window-loop regression passes. Empty stored dependencies still
check presence, declared size and SHA; removing their scratch allocation does
not bypass integrity. Failed reconstruction does not change authoritative bytes.

## Compatibility and lifecycle

A1's tested contract is active: supported store ceiling seven, raised durably
before publication of any graph containing Model, including a Concat parent.
Existing kinds remain at floor six. Merely opening/enabling does not upgrade.
A crash can leave floor seven without an accelerator; it cannot publish a Model
root before that floor. Old format-6 local binaries refuse even in off mode.
Retirement cannot lower the floor; operators must not edit FORMAT to downgrade.

The final production source SHA is
fbf324bdf60f0f760be6b811bc0d8eef88a24bc6f9268001234ff98dbe289b2f.
Its real pre-I0/I0/I1/candidate matrix repeats A1: 70 primary checks, 12 snapshot
checks, 47 actual local floor refusals plus one unavailable pre-I0 CLI flag.
Full/delta portable imports restore exact bytes without inheriting optional
roots/floor. Old wire clients sync/replicate through compatible servers.
Five actual SIGKILL/restart publication boundaries pass; nested Model publication
has an additional production unit test. Five direct ordinary-PUT crash tests
also preserve the prior namespace, GC safely and retry exactly. Compaction, tier moves, GC, repack,
doctor/retirement, snapshot deletion and history pruning preserve exact content.

All three frozen Model-hop vectors are reused unchanged from the draft archive;
managed vectors belong to A4. Five model revisions exercise hops 1/2/3 and reset
selection below the maximum. A separate test deletes/prunes the source base
object/version, applies GC and repacks: derived tensor dependencies remain exact.
GGUF fidelity and unsupported-version CDC fallback pass. Short writers and
ambiguous tensor names fail safely. No canonical case was removed or skipped.

## Independent real breadth and isolated latency

Added real sentence-transformers/all-MiniLM-L6-v2 Safetensors, revision
1110a243fdf4706b3f48f1d95db1a4f5529b4d41, 90,868,376 bytes,
SHA256 53aa51172d142c89d9012cce15ae4d6cc0ca6895895114379cacb4fab128d9db.
This is an independent family, not an invented real training history. Its actual
S3 publication, complete/range reads, serialized proof and off/shadow/prefer
restarts pass. Public ranges remain ordinary CDC behavior; no private range-speed
claim is made. Tiny GGUF is a fidelity fixture, not a throughput benchmark.

Three preselected fresh-process cohorts, no concurrent tests/producers, fifteen
full byte-equal cold reconstructions. Ordinary CDC ingest/namespace commit occur
first and are timed separately. Encoding includes CAS writes/hashing/transforms;
proof, flush, root serialization, decode and publication are separate phases.
Compiler/download/producers excluded; OS page cache is not flushed.

| Input | Optional encode s | MiB/s | Full proof s | Cold decode s | Incremental encode RSS MiB |
|---|---:|---:|---:|---:|---:|
| Pythia-14M step1000 | .1360 | 197.4 | .1147 | .1152 | 7.13 |
| step2000 | .2494 | 107.6 | .2040 | .2064 | 11.07 |
| step3000 | .3581 | 74.9 | .2636 | .2679 | 13.03 |
| MiniLM-L6-v2 | .5644 | 153.5 | .3648 | .3607 | 6.75 |

Times are medians; RSS is maximum sampled phase-entry increase across trials.
10 ms samples can miss short peaks. Cohort process maximum RSS is 66.77 MiB.
A fresh compiled-test 1 GiB control proves exact reconstruction with 94.74 MiB
process maximum RSS, 77.12 MiB peak heap growth and 11.214 s analyzer+CAS+proof.
Its 12.005 s process wall includes sparse fixture creation/hash, excludes compiler;
it is a memory control, not isolated PUT throughput. Earlier full-suite/focused
GiB measurements are retained rather than substituted for the fresh child.

Strong negative: optional uncompacted MiniLM disk growth is 91,096,054 bytes on
top of retained CDC, and Pythia residual sidecars also add physical bytes before
packing. This tranche does not claim additive storage convergence; B2 researches
that separately and remains DRAFT_ONLY. More hops also increase decode cost.

## Validation, failures and next

Full root suite passes (217.043 s); focused Model race suite passes. Two 30 s
fuzz runs pass: 189,065 recipe and 235,814 metadata executions. Root vet and diff
checks pass. The first full suite failed four stale kind/version assertions;
those logs are retained. Contract correction adds kind 8 / format 7 positives
and keeps rejection of unknown kind 9, unintegrated 11/12 and future format 8.
No implementation failure was relabeled as a pass. Replay's pre-fix failure and
range-accounting regression are retained with the repaired results.
Scripts, provenance, complete logs and phase rows are in ../z4/testdata/a2-model.
Next A3: mechanically integrate the supported SST/Parquet tranche, floor eight,
and make the required parse/semantic/components/proof/publication latency decision.
