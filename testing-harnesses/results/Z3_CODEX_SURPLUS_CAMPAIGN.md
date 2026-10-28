# ZeroS3 surplus campaign — live progress

Main before Q1 merge: `b050175029f8a484db1e97c6678c863b4d4984dd`.
Q1 branch: `z3/i1a-representation-runtime`; PR #72 passes final gates and is ready to merge.
Published checkpoints: `20519bb`, `6adfc67`, `0691572`, `324c250`.
The campaign continues automatically across batches; hourly wakes are recovery
for usage-limit interruptions, never normal batch stop points or second workers.

## Q1
Reduced full-scan allocation 26.3%; bounded root-file reads; shared healthy
liveness tables; released unused Verify lengths; whole GET chooses CDC while
representation ranges/direct verification remain. Transfer roots follow durable
sync/native commit or full/delta snapshot publication. Mode-aware import CLI.
Transfer mode/failure/reuse/CLI and twelve crash cases pass. Final black-box
compat/bundle/lifecycle/crash and race gates pass. Full repository suite, vet and formatting pass.
Baseline timing-sensitive interruption/reuse fixtures now synchronize on actual
fetch/upload progress; all original state/byte/reuse assertions remain. Decoder
corruption/concurrency coverage remains explicit under CDC whole-read policy.
Isolated 1 GiB: prefer whole GET 2.7 → 1.1 s; server peak 161 → 143 MiB;
deep verify 216 → 218 MiB (unresolved, no RSS-win claim). Verdict I1A_PARTIAL.
No production dependency, kind, wire, store/default change.

## Queue
After final gates, merge Q1 if eligible and start Q2 from latest origin/main.
Q2: mechanically port reserved Gzip 6 and Zstd 7, bounded streaming analyzers,
nested gzip/zstd→tar→SQLite/CAR, fixed lab-fixture fidelity, strict fallbacks.
Preliminary type-aware extraction compiles independently in scratch; it has not
entered production or been claimed integrated. The lab decoder adapters already
exist in repz3/kinds/gzip.go and zstd.go; reuse those exact serialized contracts.
All later queue entries remain unfinished. No research promotions/kills yet.
New kinds >=8 / ZS3R/ZS3T changes remain draft-only; CDC remains authoritative.
