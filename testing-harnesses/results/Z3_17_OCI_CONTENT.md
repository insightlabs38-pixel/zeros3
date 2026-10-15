# Z3-17 — OCI registry content and image layers (decision record)

Laboratory result; no production file changed (`zeros3.go`, root `go.mod`, manifests, journals, packs/stores, descriptors, native-ID
indexes, read paths). Code: `testing-harnesses/z3/ociz3`, `fstate`, `cmd/z3oci`; pins in `realworld/oci.json`; the Z3 module is stdlib-only.
Nothing here is permission to productionize.

**Question.** A registry stores gzip layers by digest; a layer rebuilt from the same files gets a new digest and shares almost no bytes
under CDC. Can ZeroS3 store OCI content so a rebuilt or recompressed layer costs ≈ its real change, and when does that stop paying?

**Conclusion.** Registry-blob granularity is already almost all the reuse for *unchanged* layers (`cdc` within 0.1 % of an atomic-blob model;
mapping overhead ≤ 0.06 %). The exact tar+gzip transform is a large win when a changed layer is related to its predecessor and a loss
or wash otherwise, and the deciding fact (relatedness) is not known at first sight. No static transform policy is supported; the evidence
supports the transform as an *admitted-by-evidence* tier, not a default.

## Model and exactness

`ociz3` reads an index / manifest list / **direct image manifest** (OCI or Docker schema 2; `ClassifyRoot`), config and layers. Every descriptor
is size- and SHA-256-checked before its blob is trusted; DiffIDs are verified by bounded streaming inflate; JSON bytes are never regenerated.
A digest is one node with one verified byte string: a further descriptor of the same digest that disagrees on size or kind makes the node
opaque (`conflict`), never reinterpreted. Missing, mismatching, unknown-type, zstd, attestation, `sha512` or unparsable content is an opaque
blob; the image stays representable. Limits: 4 MiB documents, 4096 descriptors, 100 000 blobs, depth 8, 256 MiB / 128× inflation guard.
Every non-oracle row (baselines and snapshot representations included) is built, serialised, deserialised and rebuilt from stored
components; length, SHA-256 and bytes compared; a non-oracle plan without a reconstruction path fails. No exactness failure in any cell
(570 synthetic, 300 real). Malformed layers (truncated, bit-flipped, not gzip, bomb, two members, junk) fall back to exact CDC in bounded time.
Real evidence: 3 pinned image families on quay.io / mcr.microsoft.com, amd64 + arm64, 36 layer references, every DiffID `ok`; no zstd,
attestation, or equal-DiffID/different-digest pair exists in the corpus (Docker Hub and ECR Public were rate-limited).

## Result 1 — synthetic revisions: cost of the revision on a stored 3-layer base (production pack rule)

| revision | new blob bytes | cdc | cdc-archive | layer-gzip-exact | layer-tar+gzip | tar+gzip-cdc | oracle-logical |
|---|---:|---:|---:|---:|---:|---:|---:|
| D1 one file changed | 228.6K | 205.9K | 287.3K | 84.4K | **15.7K** | 15.7K | 81.5K |
| D4 member reorder | 229.3K | 229.2K | 288.8K | 205.6K | **24.6K** | 24.6K | 192.4K |
| D6 gzip header only (same DiffID) | 283.1K | 55.2K | 89.0K | **3402** | 17.1K | 17.1K | 927 |
| D7a recompressed by GNU gzip -6 | 262.1K | 262.1K | 269.1K | **5772** | 19.4K | 19.4K | 927 |
| D8 tar layout PAX → GNU | 283.5K | 283.6K | 285.0K | 303.0K | **46.7K** | 46.7K | 286.0K |
| D2 one file appended | 230.1K | **9806** | 89.3K | 37.7K | 15.4K | 15.4K | 35.4K |
| D9 sparse 16 B edit in a 1.5 MB file | 634.5K | 400.4K | 430.0K | 104.6K | 626.8K | **106.1K** | 100.8K |
| D10 3000 tiny files, one changed | 199.8K | 142.5K | 212.2K | **27.5K** | 114.2K | 114.2K | 19.7K |
| D11–D14 config / annotation / index / layer order | ≈1K | ≈1K | 52–89K | ≈1K | ≈1K | ≈1K | ≈1K |

Positive: identical DiffID with different gzip bytes (D6, D7: GNU gzip, CPython zlib, Go levels 1/9) is invisible to a registry and to CDC
(260–309K new) but nearly free with exact DEFLATE (3.4–19.6K); tar-metadata / reorder / one-file rebuilds drop from 205–288K to 16–47K.
Negative: appended file (CDC reuses the gzip prefix), whiteout layer, tiny-file layers (per-member recipe 107K), and a sparse edit with members
kept whole (626.8K; chunk inside members: 106.1K). First-sight premium on the standard base: +18.6 % (break-even 0.2–1.4 revisions for
rebuild-class changes; D9 never with whole members; D10 14 revisions with a 190 % premium).

## Result 2 — real sequences: cumulative cost vs `cdc` (3 releases through one store)

| representation | node-exporter amd64 | node-exporter index | runtime-deps amd64 | runtime-deps index | cross-platform |
|---|---:|---:|---:|---:|---:|
| `cdc` (absolute) | 33.0M | 64.1M | 6337.7K | 12.2M | 21.9M |
| `archive-tar` | +0.0 % | +0.0 % | +0.1 % | +0.1 % | +0.0 % |
| `reg-blob` (oracle) | −0.1 % | −0.1 % | −0.1 % | −0.0 % | −0.1 % |
| `layer-tar+gzip` | +4.4 % | +2.5 % | **−26.8 %** | **−28.1 %** | +4.1 % |
| `layer-tar+gzip-cdc` | +2.2 % | +0.2 % | −25.9 % | −27.4 % | +4.4 % |
| `oci-auto` (greedy) | +0.0 % | +0.0 % | +0.0 % | +0.0 % | +0.0 % |
| `oci-auto-s10` (first-sight bound 10 %) | +1.2 % | +0.9 % | −25.9 % | −27.4 % | +3.7 % |

Positive: runtime-deps 8.0.1 → 8.0.2, one changed 1.0 MB layer: **1002.3K under CDC, 15.4K transformed (−98.5 %)**; 77 % of the image is already shared
by digest, CDC adds nothing inside the new blob (0.02–0.4 %). Negative: node-exporter's changed layer is one 19 MB Go binary: +3.1 % with whole
members; only the patch step gains (−5.1 %, chunked) and the +6…9 % first-sight premium is not repaid in two steps; the two architectures share
nothing (cross-platform +4 %). The greedy chooser never bootstraps (a layer first stored as CDC leaves no plaintext to compare), so the
decision problem is first sight, not the decision on a changed layer.

## Reads and cost of the transform
* Member read from a transformed layer: 12.5K for a 12 KB file vs 653.8K compressed-prefix inflate; a manifest costs 947 B as a blob vs 71.8K inside an archive chunk.
* Serving the registry-format blob needs all components (7.5 MB for a 3.3 MB layer, 820 reads) and a full re-deflate at ≈ 6.5 MB/s of tar; analysis 4.3–5.7 MB/s; peak RSS ≈ 11× the tar; layers beyond the 32 Mi token bound (70–140 MiB of tar) fall back to CDC.

## Admission
1. An unchanged digest is never re-planned; manifests, configs, indexes, unknown media types are always CDC.
2. Transform only layers passing the guard (well-formed, ≤ 256 MiB, ≤ 128× inflation) with exact reconstruction; chunk large members.
3. Compare transformed vs CDC against the current store: correct in every measured case once the predecessor's plaintext is stored.
4. First sight needs a policy: greedy never starts; a fixed 10 % bound gains 26–27 % on related content and loses 1–4 % on binary-heavy content (break-even `q*` ≈ 0.28 for runtime-deps). Carry-forward: a late-bootstrap policy (re-store when a changed layer proves related).
5. `srev.StructuredRevision` needed no new fields for Git or OCI (`lab.RevisionOfPlan` serves both).

## Production-portability status

* Reusable largely unchanged: descriptor / DiffID verification, `ClassifyRoot`, duplicate-descriptor conflict handling, bounded parsers and limits, layer guard, member/blob classification logic.
* Needs Store/CAS wiring: `Store.Blob` and `Node.Data` are whole-blob `[]byte` (verify by streaming size + SHA-256 instead); registry client caching and fetch budget; digest → component-list mapping (32 B/digest, ≤ 13K per image state) in the native-ID index.
* Deliberately lab-only: synthetic builder and producers, scenario matrix, reports, the in-memory `fstate` file model, `layer-oracle-*` rows.
* Still needs redesign: the exact-DEFLATE path materialises the layer and its tokens (no streaming, token cap, ≈ 6.5 MB/s); first-sight admission; range reads through `ir.MaterializeRange` (not wired, not measured).

## Limitations
Three families, 4–22 MB each; one changed layer carries the runtime-deps gain, so relatedness is a property of that package layer. D6/D7
producers other than GNU gzip and CPython zlib are the Go compressor that also produced the base. Timings are indicative (shared 4-core container); sizes are exact.

## Reproduce and validate
```
cd testing-harnesses/z3
go run ./cmd/z3oci fetch -group nodeexp,rtdeps -max-mib 150 && go run ./cmd/z3oci walk -group rtdeps
go run ./cmd/z3oci synth && go run ./cmd/z3oci real && go run ./cmd/z3oci reads -group rtdeps -name 8.0.1
gofmt -l . && go vet ./... && go test ./...
```

Validation: deterministic suites for conflicting / duplicate / repeated descriptors, direct-manifest and index roots, malformed and unsupported
roots, bounded mutation of root / manifest / config / layer with repaired descriptors (the mutated object is checked to be reached), broken
integrity rejection, and the standard representation set rebuilding through its recipe. `z3oci synth` was re-run after the stricter
exactness check and reproduces the D1–D15 rows above; the real-corpus matrix was not re-run (accounting is unchanged and no new download was needed).
