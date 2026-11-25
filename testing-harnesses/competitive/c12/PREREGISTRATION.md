# C12 · Format-neutral delta coding

Compare C11 CDC, generic graph optimization and native XD frames with
xdelta3, bsdiff and zstd patch mode. Run both a stored history comparison
and an isolated one-base patch comparison. Charge indexed physical files,
the exact base, complete patch framing and all production metadata.
Keep the isolated patch accounting distinct from the S3 store result.

Test two mechanisms derived from different failure modes. A locally
aligned XOR residual can encode dispersed changes inexpensively when
the same-position source still explains most bytes. Select among raw,
compressed literal, copy and residual frames. Literal fallback must
retain arbitrary bytes and contain the work on unrelated files.
Separately, factor the same native COPY/ADD plan into control, relative
copy addresses and literal streams, each deflated independently only
when it saves space. This tests the program representation directly.

Use 64 KiB indexed, independently hashed output frames in both
prototypes. Compare to the existing XD frame target under the same
base and sources. Verify whole output in a fresh process and 128
deterministic 4 KiB ranges per seekable result. Exercise corruption,
wrong bases, truncation and output bounds. Record encoding and restore
CPU/RSS, random-range p50/p95/p99, all physical bytes, and CPU per GiB
of input. Whole-file external decoders establish their whole-read cost.

Use frozen TypeScript release wrappers and decoded tar streams, C1
SQLite, compiled binary, tensor, shifted and entropy controls, and
bounded synthetic sparse-volume and append/reorder event revisions.
Hold HTTPX sdist tar bytes out of candidate iteration. Keep upstream
data hashes and same-base comparison inputs stable. Limit bsdiff to
inputs at or below 8 MiB and keep the other fixtures below 32 MiB.

Promote only a repeatable allocation or CPU gain that pays for the
base, frame/index digests, random reads and production bookkeeping.
Measure a focused redesign if the strongest prototype reveals a
specific bottleneck. New persisted representations need backward-safe
versioning and a complete authority reachability and transfer proof.
