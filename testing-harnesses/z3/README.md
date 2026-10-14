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
ir/           block-local exact reconstruction IR (skeleton + units), explicit codecs E1/E2,
              predictor + corrections (Z3-06/07)
cmd/z3deflate characterize the parser/replayer over a corpus
cmd/z3ir      unit-survival, entropy, family-cost, predictor and chooser reports (Z3-06/07)
cmd/z3fetch   opt-in pinned real-world samples (realworld/sources.json); gzip/zip/OCI/Hugging Face groups
oracle/       opt-in external research oracles (preflate-rs wrapper + setup.sh) and producers (miniz_oxide, python venv)
st/           strict stdlib-only Safetensors parser, bounded reads, exact rebuild, builder (Z3-09)
tdelta/       simple exact tensor-delta baselines (xor, planes, sparse, float-split, ...) (Z3-09)
cmd/z3st      Safetensors characterisation: identity, bits, spacing, blocks, delta, class, header, base, reshard (Z3-09)
tdelta/       (Z3-10) also: PXR native byte-plane XOR range codec, row-aligned reuse recipe, per-tensor admission, whole-file Safetensors container
cmd/z3tdelta  Z3-10 measurements: pairs (CDC / standalone / DEFLATE plane-XOR / PXR / hybrid), family (base policy, chain depth), sweep (row reuse), perf
tarz3/        exact tar structure parser: structure bytes vs member payloads, Split/Join, fail-safe UnsupportedError (Z3-11)
cmd/z3tar     Z3-11 measurements: parse, churn (what the plaintext CDC floor is made of), range, admit
binref/       format-independent binary substrate: regions + reference slots, x86 scan, one Normalize/Reconstruct (Z3-12)
elfz3/        bounded ELF64-LE parser -> binref.Image (Z3-12)
gopcln/       Go function table (pclntab) reader: unit starts/names/entry-offset slots, ELF and PE (Z3-12)
pez3/         bounded PE32+ parser -> binref.Image: sections, base relocations, imports/exports/.pdata RVAs, opaque certificate overlay (Z3-13)
carz3/        bounded CARv1/CARv2 parser (CIDs, DAG-CBOR header subset, sorted indexes), exact block-set recipe, streaming reader, deterministic CAR families (Z3-14)
cmd/z3car     Z3-14 measurements: fetch (pinned go-car samples), families, real, ident, read, stream
cmd/z3bin     Z3-12/13 corpus builder (go build -overlay, gcc/clang C) and measurements: pair, attr, delta, runs, report, verify; lab-only Mach-O view
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

## Block-local reconstruction IR (Z3-06) and predictive reconstruction (Z3-07)

`ir.Encode(stream, cfg)` splits the exact DEFLATE reconstruction metadata of a parsed stream into a **skeleton**
(block table with exact dynamic headers, stored/tail padding, a unit index, stream length + CRC-32) and
content-addressable **units**: runs of the literal/match token sequence *without literal byte values* (those live in
the semantic plaintext). Units cut the metadata, never the bitstream; the bits of a block are a function of its header
and tokens, so `ir.Reconstruct(skeleton, units, plaintext)` replays the original bytes exactly (length and CRC are
checked before a byte is written). Cut policies (`ir.Policy`): whole stream, native block, block groups, content-defined
block groups, and token-run units cut at rolling-hash positions of the plaintext (default 1-64 KiB).

| codec | unit representation |
|---|---|
| `E1` | byte oriented: 16-bit control word (length symbol, distance code, length extra) + varint literal run + distance extra bytes |
| `E1Flate` | E1 through `compress/flate` (baseline) |
| `E2` | adaptive binary range coder over the same fields, recent-distance cache, models reset per unit |
| `Pred` | the decoder rebuilds LZ77 candidates from the plaintext (32 KiB hash chain, per profile); the unit holds only corrections: match flag, candidate index, length delta, explicit E2 escape |

Lab representations (all rebuilt from skeleton + units + semantic plaintext chunks only, compared by length, SHA-256
and bytes): `ir-mono-e2`, `ir-block-e2[-pu]`, `ir-group256k-e2`, `ir-cblk64k-e2`, `ir-tok{1,2,4,8,16,64}k-e2`,
`ir-tok16k-e2-pu` (`-pu`: plaintext cut at unit boundaries instead of CDC), `ir-tok16k-e1[f]`, and the predictive
`irp-mono`, `irp-tok{1,2,4,16,64}k` (profile chosen per stream from a sample).

```sh
go run ./cmd/z3ir units   -corpus "$TMPDIR/z3c" -families 'deflate-L6-*'   # unit survival per policy
go run ./cmd/z3ir entropy -corpus "$TMPDIR/z3c"                             # IR size and entropy per codec
go run ./cmd/z3ir family  -corpus "$TMPDIR/z3c" -ref-bytes 35 -json r.json  # cost with plaintext/unit/ref breakdown
go run ./cmd/z3ir predict -corpus "$TMPDIR/z3c" [-ablate]                   # predictor corrections, reset penalty
go run ./cmd/z3ir chooser -from r.json,p.json                               # CDC vs explicit vs predictive
go test ./ir ./lab -short                                                   # round trips, malformed, locality
go test ./ir -run '^$' -fuzz '^FuzzReconstruct$' -fuzztime 60s              # also FuzzUnitDecode/FuzzSkeleton/FuzzEncodeDecode
```

Results: `../results/Z3_06_DEFLATE_IR.md`, `../results/Z3_07_PREDICTIVE_RECONSTRUCTION.md`.

## Z3-08: generalization, streaming, production-shaped evidence

```sh
# real DEFLATE families (opt-in downloads, hash-verified, cached outside Git)
go run ./cmd/z3fetch -group deflate -out "$TMPDIR/rw" -max-mib 800        # wheels, sdists, jars, apks, crates, npm, GNU tarballs, Go src, OCI layers
go run ./cmd/z3ir family -corpus "$TMPDIR/rw" -reps cdc,preflate,oracle-logical,irp-tok16k,irp-tok16k-bal -ref-bytes 35
# producer matrix: real plaintext revisions recompressed by every installed producer
oracle/producers/setup.sh                                                  # python venv (zlib-ng, ISA-L, libdeflate, zopfli) + miniz_oxide, outside Git
go run ./cmd/z3corpus extreal -out "$TMPDIR/ext" -label gosrc -plain a.tar,b.tar,c.tar -mib 6   # Z3_PYTHON / Z3_MINIZ select producers
go run ./cmd/z3ir producers -corpus "$TMPDIR/ext"
# zlib levels 1-3 (exact deflate_fast emulation), tiny blocks, repeated distances, speed modes
go run ./cmd/z3ir fast -plain big.tar -producers zlib1,zlib2,zlib3,gzip1
go run ./cmd/z3corpus plain -out "$TMPDIR/rep" && go run ./cmd/z3ir predict -ablate -corpus "$TMPDIR/ext2" -group-by kind
go run ./cmd/z3ir modes -corpus "$TMPDIR/ext"                              # ratio / balanced / fast
# streaming, ranges, recipes, lifecycle
go run ./cmd/z3ir stream -base big.tar -mib 64,256,1024 -producer gzip6
go run ./cmd/z3ir range -gz some.gz
go run ./cmd/z3ir recipe -from report.json -reps cdc,irp-tok4k,irp-tok16k,irp-tok64k
go run ./cmd/z3ir lifecycle -from report.json
```

Results: `../results/Z3_08_DEFLATE_GENERALIZATION.md`.

## Z3-09: Safetensors characterisation

```sh
go run ./cmd/z3fetch -group pythia14m-train,pythia14m-sft-koshirowada,pythia14m-logs-honicky,bert-tiny-family,pythia14m-siblings -out "$TMPDIR/st" -max-mib 1500
go run ./cmd/z3st inspect  -corpus "$TMPDIR/st"      # structure and the I/O of a bounded parse
go run ./cmd/z3st identity -corpus "$TMPDIR/st"      # tensor identity, renames, header/data order
go run ./cmd/z3st bits     -corpus "$TMPDIR/st"      # bit-identical elements, XOR sparsity, sign/exponent/mantissa
go run ./cmd/z3st spacing  -corpus "$TMPDIR/st" -families rw-pythia14m-train
go run ./cmd/z3st blocks|delta|class|header|base|reshard -corpus "$TMPDIR/st"
go run ./cmd/z3ir family -corpus "$TMPDIR/st" -reps cdc,struct-seg,struct-cdc,delta-xorz,delta-plane,delta-sparse,delta-float,delta-best
go test ./st ./tdelta; go test ./st -run '^$' -fuzz '^FuzzParse$' -fuzztime 60s
```

Results: `../results/Z3_09_SAFETENSORS_CHARACTERIZATION.md`.
