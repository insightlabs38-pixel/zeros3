# Z3 research laboratory

Z3 asks whether structure-aware *physical* representations can beat current
ZeroS3 CDC/CAS reuse for objects whose bytes hide reusable content. User-visible
object bytes stay exact; nothing here changes the store format, manifests,
journal or `zeros3.go`.

This directory is a separate stdlib-only Go module (`zeros3-z3`). It is
research code, not a production contract. Generated corpora are scratch data
and are never committed.

```text
corpus/      deterministic revision-family generator + manifest
cmd/z3corpus gen | verify | list
```

## Corpus

```sh
cd testing-harnesses/z3
go run ./cmd/z3corpus gen -out "$TMPDIR/z3c"            # ~120 MiB at defaults
go run ./cmd/z3corpus verify -dir "$TMPDIR/z3c" -regen
go run ./cmd/z3corpus gen -out D -size-kib 64 -families 'zip-*,st-*'
```

Knobs: `-seed`, `-size-kib` (base artifact size, 32..262144), `-revisions`
(1..16), `-families` (globs), `-max-mib` (hard output bound). A corpus is a set
of *families*; each is a chain `r0..rN` where every revision mutates its parent.
`manifest.json` lists family, format, workload, revision, mutation, path, size,
sha256, parent/base and `truth` (known unchanged/changed/added/removed members,
tensors, records, blocks).

| Class | Families | Subset notes |
|---|---|---|
| raw controls | `raw-{append,insert,delete,sparse-edit,reorder,random}` | |
| DEFLATE | `gzip-L{1,6,9}-insert-mid`, `gzip-L6-{insert-begin,delete,replace,append,repetitive,incompressible}` | Go `compress/gzip`, zero mtime |
| ZIP | `zip-{stored,deflate}-{change,add,remove,reorder,meta}` | own minimal writer (no data descriptors, no zip64); `archive/zip` reads it |
| Safetensors | `st-*` | spec layout; BF16/F16/F32; padded header |
| GGUF | `gguf-{metadata,modify-tensor,requant,repeated}` | v3 subset: u32/f32/string metadata, 1-D tensors F32/F16/Q4_0/Q8_0, alignment 32, no arrays |
| PyTorch ZIP | `torch-{update-some,add-storage,pkl-only}` | stored, 64-byte-aligned members; `data.pkl` is opaque random bytes, never unpickled |
| binary rebuild | `bin-{rebase,insert-func,meta,semantic}` | synthetic "ZBIN": 16-byte bundles with rel32 calls, abs64 loads, pointer table |
| CAR | `car-{reorder,roots,add,remove,overlap}` | CARv1, CIDv1 sha2-256, raw + dag-cbor link nodes |
| structured controls | `pagedb-*`, `colstore-*`, `ndarray-*`, `protorec-*` | characterisation shapes only, no SQLite/Parquet/HDF5/ONNX claim |

Determinism: all randomness is an in-package SplitMix64; floating point is
never used. Families that depend on Go's DEFLATE implementation are flagged
`toolchain_dependent`; `verify -regen` reports a mismatch on those as a warning
only when the manifest was produced by a different Go release.

Not generated: real PE/ELF fixtures (optional in the batch plan; no compiler is
required by this module).
