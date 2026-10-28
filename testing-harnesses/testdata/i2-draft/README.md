Q9 DRAFT_ONLY contract vectors. Regenerate from the lab module:
`go run ./cmd/i2-fixtures ../testdata/i2-draft`.
The generator mechanically reuses I1B's envelope translator; recipes/component
bytes remain identical, only the existing envelope and derived-child addresses
change. Two D8 DEX and three Roslyn CLR producer fixtures retain their original
module testdata provenance. Three synthetic raw-tensor vectors exercise planes
and XOR base chains through3 operations. They are not model-format/market evidence.
Production decoder tests verify bytes, SHA, serialized roundtrip and reachability.
No PyTorch/JVM/Wasm/PXR analyzer or default behavior is introduced.
