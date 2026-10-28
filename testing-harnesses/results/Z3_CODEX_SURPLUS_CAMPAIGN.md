# ZeroS3 surplus campaign — live progress

Merged Q1: PR #72, `4f97c7beeaad542db854a54aa874f87a6dc6d5ff` (I1A_PARTIAL).
Merged Q2: PR #73, `bd0562101364b65e7ddec4a33bc89ab7189b6b87` (I1B_COMPLETE).
Active Q3: `z3/i1c-git-oci`, implementation/economics checkpoint.
Q2 checkpoints: `eb07532`, `a0a20ab`, final validation checkpoint in this branch.
Continue through the entire queue automatically. Hourly wakes are recovery after
usage limits, never normal batch stop points; an active worker treats a wake as
a reminder and must not start a second worker.

## Q1 carry-forward
Full-scan allocation −26.3%; bounded root loading; CDC whole GET policy;
post-commit optional sync/native/full/delta import roots and mode-aware import.
1 GiB whole GET 2.7→1.1s, server peak 161→143 MiB; deep verify 216→218 MiB:
no deep-verification RSS win, descriptor materialization remains a debt.
Full/race/compatibility gates pass; all original corruption/resume assertions kept.

## Q2
Reserved Gzip 6/Zstd 7: mechanically extracted streaming codec cores and lab
serialized adapters, temp-backed plaintext, direct CAS units, bounded metadata,
analysis depth ≤3 and existing cumulative inspection limits. Nested tar→SQLite
and CAR exact, cold semantic ranges measured, public compressed ranges use CDC.
Seven lab recipe vectors pass after translating only pre-existing envelope and
child addresses. Generator output reproduces the production codec declarations.
Lab/production predefined FSE tables now initialize indexes before concurrent use.
64 MiB stored-DEFLATE proof succeeds at 50,168 KiB peak RSS; retained best-speed
negative hits the predictor budget and falls back to verified CDC at 19,840 KiB.
Four-revision sidecars are additive; later sidecar growth ≈10 KiB/revision.
Full suite 206.4s, race 49.5s, lab tests/vet, root vet/gofmt, decoder/analyzer fuzz,
and codec-expanded wire/compat/bundle/lifecycle/SIGKILL checks pass.
No test removed/skipped to obtain PASS; namespace interruption now uses a real
fetch gate instead of a timing assumption, preserving every resume assertion.

## Next
Q3 implements bounded gp-zlib pack v2/v3 and idx v2; all eight lab vectors,
hostile CDC fallbacks and public hiredis reexports pass. Reorder shares 5109/5216
components but cumulative additive CDC+representation costs exceed CDC alone.
BusyBox registry blobs/DiffIDs verified; codec/tar composition works. Generic
ZIP Concat/Derived adapter passes framing/descriptor/ZIP64/signing/reorder cases.
Final integration gates are running; then merge eligible Q3 and begin Q4.
All later queue entries remain unfinished. No research promotions/kills yet.
No new production number ≥8, wire/store version, default or planner decision merged.
