# Native feasibility follow-up

Use the exact existing deflate, prefix and cohort-block pack bytes and
resample 4,096 reads per phase, sequentially and across eight workers.
Use the native Go inflater pool, bounded buffers and the original exact
object digest check. Compare actual bytes decoded and fetched. This
separates the algorithmic work from Python scheduling overhead.

Test a native compatibility route on private clones of the finalized
main and held-out stores. Lay out each group as a pin over one combined
CAS chunk. Point each distinct member at its range using the existing
XD COPY envelope and B9 authority records. Finalize, reopen, check each
object's SHA-256 and repeat signed S3 reads. Compare 8 and 32 KiB blocks
and a group cost gate that charges components and authority entries
before admitting a block. The production codec, claims and footprint
must include those bytes.

This store experiment is scoped to synthetic and public fixtures. A
group body is a storage feasibility test. Exporting its complete content
as a dependency would need an explicit authorization check for all
siblings. A physical layout below the CAS read API could avoid that
new authority dependency altogether.

The native-shaped pack experiment charges the existing 44-byte header
and 52-byte index entry per logical record. A grouped member uses a
12-byte slice reference and each group has its own verified extent.
The probe writes a separate fsynced test pack, reopens it, verifies
the footer and member digests, and measures both individual and
coalesced sweeps. It uses the production pack compressor and inflater
without adding an on-disk store feature.

The main and HTTPX cache experiments also reuse the production
chunk-cache policy with verified full-frame hashes. The main working
set fits in the larger trial budget, so repeat the measurements with
an independent wider source panel before drawing a deployment conclusion.
