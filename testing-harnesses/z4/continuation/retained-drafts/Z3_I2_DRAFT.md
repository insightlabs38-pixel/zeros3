# Z3-I2D — Model/DEX/CLR production draft

Status: **I2_DRAFT_BLOCKED — DRAFT_ONLY; DO NOT MERGE**. PR #80.
Model streaming prototype and codec fidelity work are reviewable. Managed
creation is deliberately disabled: whole-file masked analysis and transient
identity/reference memory do not meet this campaign's production memory gate.
Continue Q10 from main; this blocked integration lane must not consume the queue.
Candidate8/11/12 numbering is unapproved. No ZS3R/ZS3T/store/journal/default change.

## Mechanical port and scope
Type-aware extractor selects6386 managed-core lines,901 ST/GGUF parser lines,
502 Model recipe lines and137 managed adapter lines; framework calls translated
to existing production primitives. Group cutters shell-extracted from econx/lab.
Only zeros3.go is production;37850→46332 lines (+8482,22.4%). Root module/sum
unchanged; `go list -deps` reports only stdlib plus zeros3. All code stays draft.
Added function declarations:262. Three ported codec families; two major adapter
paths (streaming Model and quarantined managed), with shared group cutters reused.
Recipe/component bytes remain the lab contracts. Eight immutable producer/
synthetic vectors preserve their original recipes and groups after the existing
Z3RP-v2→ZS3R-v1 envelope/child-address translation. Both managed encoder cores
also reproduce all five D8/Roslyn fixture roots byte for byte.
No PyTorch ZIP/JVM/Wasm/PXR analyzer. CompactDex remains unsupported. ZIP/APK
composition is not enabled in production; DEX evidence uses extracted exact DEX.

## Model: active experimental analyzer
Content sniffing accepts strict Safetensors/GGUF; malformed/unsupported inputs
keep ordinary successful CDC PUTs. Headers/gaps/tails use production CDC.
ST header<=16MiB/4096 tensors/32 dimensions; GGUF metadata<=16MiB/4096 tensors/
65536 KV entries/1MiB strings. Unknown types/versions remain fallback.
Tensor planes/XOR residuals stream through256KiB windows into existing virtual
components; no whole tensor is materialized. Quantized GGUF uses exact block
widths. Metadata is bounded; source bytes reserve the existing1GiB inspection
budget, and base requests must fit its remainder. Large inputs may reset to
standalone planes. This is a prototype selection policy, not a profitable planner.
Every tensor has a closed one-op child; top-level Model COPY members preserve
names and exact output SHAs. Existing owning sections map a context digest
(format/name/type/shape) to explicit child SHAs. At most8 basis candidates are
inspected; full child/base proof remains mandatory. Catalog canonical ambiguity
semantics are unchanged. >8 candidates or missing/invalid hints mean missed reuse.
Output-digest hints use existing recipe name strings, with byte identity enforced
by the derived-member SHA during full reconstruction. Native hints are not truth.
Maximum3 tensor operations plus the file root fit existing depth4. Public Pythia
steps1000/2000/3000 (3x26.84MiB, REAL pretraining checkpoints) replay exactly at
1/2/3 operations; each4KiB tensor request matches original bytes. They do not
replace the BF16 fine-tuning/quantization breadth gate from earlier research.
Controlled5-revision ST, GGUF quantized/F32, restart, unknown-version fallback,
short writer, ambiguous names and early-budget tests pass. Base object/history
retirement plus GC/repack leaves the dependent tensor exact; the child depends
on the required tensor, not the whole earlier object. All reads are reachable.
Private tensor ranges are bounded64MiB and preserve the lab's range semantics:
physical CAS chunks authenticate reads; no whole-logical-component SHA claim
without a sequential pass. Nonempty derived frames on this path fail unsupported.
Public S3 whole/range GET policy remains CDC where this kind has no S3 range adapter.

## Memory and managed gate
1GiB chunk-distinct sparse ST control, analyzer+CAS+serialized full proof+reach:
83.04MiB peak heap growth,100208KiB process RSS,12.84s whole child process.
This is a synthetic memory control, never model-storage economics. Pythia cohort
peaks36780KiB RSS; measured per-stage heap growth8.0/9.5/19.7MiB. Timings are
co-located validation timings, not production latency comparisons.
Managed cores preserve identity-context caches, DEX K0=3 and offline CLR AutoK0
12%, content-based4KiB normalized groups and exact nodes. Ordinary PUT does not
call them. Production replay is capped32MiB output/128MiB aggregate components/
1MiB per component/65536 groups/8KiB nodes; size claims checked before allocation.
Removed research-only retained Refs/Cuts/Secs diagnostics; identical public
serialized roots confirm no contract change. Source-identical before/after proof:

|Offline cohort|Input|RSS before→after|Maximum heap growth before→after|
|---|---:|---:|---:|
|F-Droid3 releases|8.9MiB each|374.8→245.9MiB|322.9→212.0MiB|
|Newtonsoft5 releases|0.66–0.68MiB each|74.8→59.8MiB|54.1→41.8MiB|

These scopes include analysis+self-check+CAS+production decoder proof, and are
not directly comparable with the earlier29x figure. The remaining footprint is
still unacceptable for automatic managed creation. No claim of resolving it.
Two fixed-output/path-mapped F# SDK8.0.427 builds (constant3→4) also replay exactly
with AutoK0; producer/source/compiler hashes and DLLs retained. Controlled coverage,
not a natural non-Roslyn release history or an economics promotion.

## Compatibility and failure evidence
All three candidate numbers are exercised: Model via real S3 creation, DEX/CLR
via offline-proved fixture installation **after** ordinary journal commit.
No production managed creation hook is added to make this matrix pass.

|Matrix|Evidence|
|---|---|
|Pre-I0 levels1/2/3/5; off/shadow/prefer; S3 transcripts|54 checks pass|
|Model plus existing kinds through compact/tiers/repack/GC/history/restart|32 pass|
|Portable full bundles, old/new import and native replication|24 pass|
|Model-base full/delta export, exact-base verify, old/new import/restore|23 pass|
|SIGKILL with committed Model fixtures and mixed PUTs|24 pass|
|I1 reads Model8 roots in all modes; no open-time rewrite|23-check matrix pass|
|I1 reads DEX11/CLR12 roots; no open-time rewrite|23-check matrix pass|
|I1 deep verify/GC-apply/repack-apply with unknown kinds|Refuse; disk unchanged|
|I1 lossless compaction; I2 afterward|Complete graph and CDC bytes stay exact|
|I2 opens I1 stores in all modes|No upgrade or byte rewrite|

Dry-run repack is informational; the gate explicitly uses -apply. Lossless
compaction is checked for graph preservation rather than falsely requiring error.
No failing canonical case was deleted or skipped. Unknown kinds still fail closed
for destructive collection. Old binary reads do not imply old binary deep support.

## Verification and remaining work
Full root suite220.094s; I2 race70.984s (including1GiB control); full lab tests,
root/lab vet, stdlib/single-file checks pass. Mutated-root/decoder fuzz88528 cases/
26s; bounds, writer, exact fidelity, native ranges and dependency liveness pass.
Evidence under `testdata/i2-draft`; baseline diagnostic patch reproduces the
measured before-source SHA. Raw public inputs reuse pinned Q6 provenance.
Still required: ReaderAt/masked-group managed analysis and incremental replay;
managed memory caps validated at realistic scale; BF16 fine-tuning/quantized model
breadth, long-history basis policy and independent non-Roslyn release histories.
Do not merge or freeze numbering. Q9 remains a useful blocked draft; proceed Q10.
