# Z3 research laboratory

Z3 asks whether structure-aware *physical* representations can beat current
ZeroS3 CDC/CAS reuse for objects whose bytes hide reusable content. User-visible
object bytes stay exact; nothing here changes the store format, manifests,
journal or `zeros3.go`.

This directory is a separate stdlib-only Go module (`zeros3-z3`). It is
research code, not a production contract. Generated corpora are scratch data
and are never committed.

```text
corpus/       deterministic revision-family generator + manifest
lab/          representation laboratory: accounting, baselines, reports
cmd/z3corpus  gen | verify | list
cmd/z3lab     run the laboratory over a corpus
cmd/z3oracle  Preflate oracle report (Z3-04); needs the optional oracle
deflate/      native exact RFC1951 parser/replayer + gzip/zlib wrappers (Z3-05)
cmd/z3deflate characterize the parser/replayer over a corpus
cmd/z3fetch   opt-in pinned real-world samples (realworld/sources.json)
oracle/       opt-in external research oracles (preflate-rs wrapper + setup.sh)
```

```sh
cd testing-harnesses/z3
go test -short ./...        # seconds; omits the production-CDC equivalence build
go test ./...               # also compiles zeros3.go and compares CDC boundaries
```

## Corpus

```sh
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
| raw RFC1951 | `deflate-{L0,L1,L6,L9,huffonly,fixed}-insert-mid`, `deflate-L6-{insert-begin,delete,replace,append,repetitive,incompressible,multirev}` | `compress/flate` (stored/dynamic/Huffman-only) and `corpus.DeflateFixed` (fixed-Huffman greedy emitter) |
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

## Laboratory

```sh
go run ./cmd/z3lab -corpus "$TMPDIR/z3c" -json "$TMPDIR/report.json" [-per-family]
go run ./cmd/z3lab -gen -size-kib 4096 -families 'st-*,zip-*'      # temp corpus
```

For every family the lab walks the revision chain and, for each representation,
answers: *how many additional physical bytes does this revision need beyond
what that representation already stores for the family?* Each representation
maps a revision to a `Plan` (content-addressed components, recipe cost, optional
exact `Rebuild`). `Acct` charges first occurrences of a component; later
references are reuse. Costs counted: new component bytes, recipe bytes
(`-ref-bytes` per reference, default 96 = one manifest `chunkRef` JSON entry;
small regions are inlined at 4 B + length), index bytes. `packed` columns apply
the production pack rule (DEFLATE default level, kept only if it saves 1/16) to
new components.

| Representation | Meaning |
|---|---|
| `raw` | every revision stored whole (control) |
| `cdc` | **current ZeroS3 CDC v1** (copy of production; see equivalence test) |
| `fixed-64k`, `fixed-4k` | fixed-size blocks |
| `struct-seg` | cut at segment boundaries (ZIP member, tensor, CAR block, page, record...), segments stored whole, gaps CDC |
| `struct-cdc` | same cuts, CDC restarted inside each segment |
| `delta-replace`, `delta-xor`, `delta-xorz` | changed same-key, same-length segments as replacement / raw XOR / DEFLATE'd XOR against the previous revision |
| `oracle-logical*` | CDC over decoded content (gzip plaintext, ZIP member contents): an estimate, no reconstruction path |
| `oracle-refmask*` | ZBIN with reference fields zeroed (`+refs` charges them verbatim): an estimate |

Every non-oracle plan is reconstructed through the store and compared by length
and SHA-256 to the original (and sampled range reads are compared byte for
byte); a mismatch marks the row `invalid`. A representation that cannot be
built (unparsable container, limits exceeded) reports `error` for that row: an
optimisation failure, never an object-validity failure. Fields that do not
apply are `N/A` in the table and `null` in JSON.

The JSON schema (`z3-lab/1`) is experimental. Delta experiments read the XOR
base from the previous revision's file; chain walking cost is not modelled.

`TestCDCMatchesProduction` compiles an unmodified copy of `zeros3.go` with a
dump hook, then compares production CDC constants, gear table and chunk
boundaries against the lab copy over generated streams (whole and fragmented
reads). It fails on drift in either direction.

## DEFLATE oracle (Z3-04)

`z3corpus ext -out DIR` writes an *optional* corpus recompressed by locally installed producers (python3 zlib with
several levels/memLevels/strategies, GNU gzip, Info-ZIP); absent tools are skipped. It is kept apart from the
guaranteed stdlib corpus in all reports.

`oracle/preflate/setup.sh` clones and builds a pinned `preflate-rs` outside Git (provenance in
`oracle/preflate/README.md`). With it present the lab adds `preflate` (plaintext chunks + reconstruction blob as
recipe metadata) and `preflate-rcdc` (blob also CDC-chunked); without it they are not applicable and no test fails.

```sh
go run ./cmd/z3oracle -corpus "$TMPDIR/z3c" [-ext "$TMPDIR/z3ext"]
```

Results: `../results/Z3_04_PREFLATE_ORACLE.md`.

## Native exact DEFLATE (Z3-05)

`deflate.Parse` turns a raw RFC1951 stream into an explicit IR (blocks, dynamic-header encoding choices, raw
token symbols/extra bits, padding bits) plus the decoded plaintext; `Replay` regenerates the identical bits.
`ParseGzip`/`ParseZlib` keep wrapper bytes verbatim. Differential oracle: `compress/flate` (Preflate is optional).
The lab representation `deflate-ir` reports parse/replay success, structure and time; its cost is N/A until Z3-06
defines a physical encoding.

```sh
go test ./deflate                                   # golden, malformed, differential, mutation, fuzz seeds
go test ./deflate -run '^$' -fuzz '^FuzzParse$' -fuzztime 60s
go test ./deflate -run '^$' -bench . -benchmem
go run ./cmd/z3deflate -corpus "$TMPDIR/z3c"
```

Results: `../results/Z3_05_DEFLATE_PARSER.md`. Real-world samples: `realworld/README.md`.
