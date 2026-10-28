# Z3-I1C — Git baseline, OCI composition, exact ZIP side study

Status: IN PROGRESS; base `bd0562101364b65e7ddec4a33bc89ab7189b6b87`.
Kind 4 retains its I0 reservation. No new kind/wire/default decision is authorized.
No merge eligibility before final integration gates.

## Git mechanism checkpoint
The type-aware extractor now has a Git mode. It selects 84/260 declarations
(1333 source lines) from the bounded ReaderAt pack/idx mechanisms and metadata
helpers; imports are stdlib only. No Git executable or GPL delta generator enters
production. Reuse the lab kind-4 serialized table, index and gp-zlib metadata.
The lab RebuildSeq body is shell-extracted; plaintext/set branches are explicitly
unsupported. Changes are Call/source boundaries, a 64 MiB compressed-entry cap
and propagation of every output-write error. No DEFLATE recompression at decode.
Eight vectors pass: SHA-1/SHA-256, full/OFS/REF deltas, pack v2/v3, exact size,
SHA-256, bytes, native lookup and dependency closure. Six are REFERENCE_FIXTURE;
two pack-v3 header/trailer variants are SYNTHETIC_FIXTURE. As in Q2, only the
existing lab/production envelope differs; the complete pack representation body
is unchanged (no derived child addresses need translation).
The pure ReaderAt parser resolves the same native identities independently.
Native ids remain hints to compressed entry streams, never object identity.

## Next implementation work
Production bounded analyzer and stable zlib stream components; idx v2 exact
closed representation without the lab's pack-plan closure; malformed/thin/cycle
fallback; native lookup bounds; real repack/history economics; concurrency,
crash/fuzz/full/compatibility gates. OCI JSON/layer digest/DiffID validation and
codec/tar composition; exact ZIP framing side study using existing primitives.
The lab IdxPlan currently regenerates through a captured pack plan and is therefore
analysis-only as written. Do not transplant that closure into production.
No advanced Git set/plain forms planned unless measured evidence changes scope.
