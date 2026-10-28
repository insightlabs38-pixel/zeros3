# Z3-I1C — Git baseline, OCI composition, exact ZIP

Status: I1C_COMPLETE; base `bd0562101364b65e7ddec4a33bc89ab7189b6b87`.
Integration gates pass; no default/wire/store change.

## Git
Reserved kind 4 now supports exact gp-zlib pack v2/v3, SHA-1/SHA-256,
full/OFS/REF entries. Native object IDs index compressed stream components;
they are lookup hints, never content identity. Decode never runs Git or a delta
producer, reconstructs headers/OFS offsets and checks every output write.
The type-aware extractor selects 84/260 lab declarations (1333 source lines).
Parser, delta resolution and metadata reader are mechanically extracted;
RebuildSeq and serialized kind adapters are shell-copied with boundary changes.
Only ModeZ is accepted; set/plain/GPL generation remain research-only.
ReaderAt caps: pack 1 GiB, 262144 objects, 64 MiB/object, 512 MiB cumulative
inflation/delta output each, depth 64; existing cumulative analysis budget 1 GiB.
Source reads and inflated/resolved bytes are charged, including nested sources.
Decoder table <=32 MiB, compressed entry <=64 MiB; recipe count cannot exceed
available pieces, every entry needs a piece, unused pieces fail closed.
Idx v2 validates fanout/order/CRC-offset geometry/native footer, then uses exact
Concat sections. No persisted closure over a pack plan; idx v1 stays CDC.
Eight immutable lab vectors pass full bytes/size/SHA/native lookup/closure:
six REFERENCE_FIXTURE and two SYNTHETIC_FIXTURE pack-v3 header/checksum variants.
Existing lab Z3RP-v2 versus production ZS3R-v1 envelope translation only; the
complete Git representation body is unchanged. No DEFLATE recompression.
Thin, unresolved REF-cycle, checksum, truncated, count and huge-size cases all
preserve exact authoritative PUT/GET while declining optional roots.

## Public Git economics
REAL_REEXPORT: hiredis public repository, HEAD
`5bb7f030f6631b243438cf0fdd3be84352b905bb`, Git 2.52.0.
Hash-pinned files, refs, commands: `testdata/i1c-git/real-reexports.json`.
These are locally generated pack exports, not natural release artifacts.
Full/reordered/delta input sizes: 9765945 / 9749162 / 2026832 bytes.
Reordered representation shares 5109/5216 physical components with full.
Cumulative authoritative CDC vs CDC+optional representation chunk-store bytes:
full 9765945 / 19988838; reordered 19515107 / 30359509;
delta 21518028 / 33686965; all six pack/idx files 21862042 / 34261850.
All exact/deep verification passes. Reorder component reuse is real; total
production savings are absent because CDC stays authoritative and additive.
No admission/lineage state or advanced set/plain form is justified here.
Reproduce: ZEROS3_Z3_I1C_REAL_GIT=cache go test -run TestRepGitRealReexports -v.

## OCI
No KindOCI. Registry JSON/config ordinary blobs; gzip layers use Q2 exact
Gzip -> tar/Concat composition. Production has no registry network client.
Two REAL Docker BusyBox linux/amd64 tags, 1.36.1/1.37.0, independently fetched
with anonymous pull tokens kept in memory. All index/manifest/config/layer native
SHA-256/size and complete config DiffIDs verified. Cache reuse rechecks hashes.
Provenance: `testdata/i1c-git/real-registry.json`; fetch/verify:
`python3 scripts/i1c_fetch_oci.py cache-directory` (8 MiB/blob, 20 MiB corpus,
32 MiB decoded layer). Download auth is stripped on cross-host redirects.
Layers 2206402/2211514 bytes, plaintext 4648960/4659200; full DiffIDs match
production decoded children. Cumulative CDC vs CDC+rep: 2207471/6926239 then
4420054/12703528. Additive overhead, no storage-win claim.
Lab VerifyLayer streams raw/gzip/zstd identities under bounds; unknown media
is native-hash-verified opaque. Fixed old VerifyDiffID prefix-limit acceptance;
full plaintext is required, with regression for a matching bounded-prefix hash.
Corruption, wrong DiffID and inflation-limit cases pass.

## Exact ZIP side study
Reusable lab `repz3/kinds.EncodeZIPAt` uses serialized Concat/Derived only.
Stored members may derive existing child kinds; method-8 compressed bytes and
opaque signing gaps remain exact components. No new kind or wire semantics.
Stored/DEFLATE, descriptors, ZIP64 32/64-bit descriptors, central directory,
reordered members and opaque signing bytes pass exact bundle replay and closure.
Go archive/zip provides an independent extraction oracle where applicable.
Default old torch parser stays strict stored-only; generic framing is opt-in.
Adapter is production-shaped, not installed as a root analyzer: lab bundle
collection needs CAS callbacks at port. Raw DEFLATE normalization is deferred.

## Integration
Stdlib-only production remains one file: 36146 -> 37850 lines (+1704).
No root module/sum changes; kind 4 reservation reused, unsupported now >=8.
All lab Git/OCI/torch/representation-kind suites and vet pass (25.4/4.7/0.01/87.8s).
Race Git/OCI/recipe/fuzz seeds plus real interruption tests pass (7.436s).
Decoder fuzz 180056 cases/25s; ReaderAt rerun 103409/25s (initial run spent
its budget minimizing mutations; retained evidence, then bounded minimization).
Expanded I0 black-box checks pass: compatibility 54, bundles 24, lifecycle 32,
SIGKILL 24, with codec+Git fixtures. Every original assertion retained.
Initial full suite failed namespace retry through a reverse proxy (truncated
bulk frame). Fixture proxy now controls only the killed attempt; uninterrupted
retry uses the actual source endpoint. Targeted race and three repeated runs
pass; final full suite passes in 179.865s. Final root vet/gofmt pass.
