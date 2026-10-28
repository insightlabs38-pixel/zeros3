Q9 DRAFT_ONLY contract vectors. Regenerate from the lab module:
`go run ./cmd/i2-fixtures ../testdata/i2-draft`.
The generator mechanically reuses I1B's envelope translator; recipes/component
bytes remain identical, only the existing envelope and derived-child addresses
change. Two D8 DEX and three Roslyn CLR producer fixtures retain their original
module testdata provenance. Three synthetic raw-tensor vectors exercise planes
and XOR base chains through3 operations. They are not model-format/market evidence.
Production decoder tests verify bytes, SHA, serialized roundtrip and reachability.
No PyTorch/JVM/Wasm/PXR analyzer or default behavior is introduced.

Final status: I2_DRAFT_BLOCKED; ordinary managed creation remains disabled.
validation.json.gz contains source/input SHA pins, public/model/managed/F# proofs,
RSS/heap evidence, before/after identical roots,1GiB memory scope and full matrix
counts. Logs retain full test/compatibility evidence. managed-diagnostics-before.patch
reproduces the before-source SHA on an isolated copy of final zeros3.go.
q9_probe.py builds a private main alongside mechanically copied exact production
source (only main renamed), then records isolated child RSS; it is not a decoder
runtime dependency. The optional KIND@EMPTY_STORE mode installs offline-proved
managed fixtures after ordinary journal commit for rollback tests only.
q9_build_fsharp.py reproduces two controlled builds with fixed output/path maps;
SDK8.0.427 compiler/core hashes are in validation.json.gz. r0/r1 DLLs are retained.
No natural-history, latency or storage-promotion claim follows from these controls.
All production kind numbers remain draft. See Z3_I2_DRAFT.md for unresolved gates.
