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

## Results from the native reader and wide panel

The authority graph compatibility route grew the main store from 1,773,568
to 1,871,872 bytes using 32 KiB groups. The fully priced gate admitted zero
groups. HTTPX also showed no net gain through that existing representation.
Neither route became a production feature.

The wider panel contains 4,926 unique TypeScript 5.7.2 and RuboCop 1.66.1
fragments and 14.32 MB logical bytes. In the same archive layout as the
earlier tests, exact deflate used 4,472,832 allocated bytes, gated prefix
4,046,848, and 32 KiB blocks 3,272,704. In native Go at eight clients with
4 MiB caches, first-pass p95 was 27.8 µs independent, 26.7 µs gated prefix,
51.4 µs for 8 KiB blocks and 175.1 µs for 32 KiB blocks. More decoded bytes
per lookup persisted on this working set after accounting for the Python
runtime and using the production cache policy.

In the shadow pack with native record widths, independent fragment
allocation was 4,726,784 bytes. Grouping at 32 KiB used 3,715,072 and
coalesced sweeps fell from 97.4 ms to 68.8 ms. Separate member sweeps rose
from 101 ms to 462 ms. This provides a reason to revisit bounded groups for
the fused-reader experiments in C17. Ordinary small-object read latency
sets a different limit.

## Finalized native preset packs

The file-backed trial used the same raw compacted seed for both lanes and
rotated order twice. It checked the entire signed S3 corpus with whole and
4 KiB range reads after GC and deep verification. Each admitted pack pays
for two local context copies and 192 bytes of record and index overhead.
The sampling gate compresses up to 1,024 verified small records and requires
the observed savings to cover the whole context. Individual preset records
also have to beat independent DEFLATE. An unprofitable pack keeps ordinary
encoding. Context allocation is at most 32 KiB per copy, and preset members
are at most 4 KiB each.

| Scope | Ordinary bytes | Explicit digest | Pack-local context | Ordinary / final repack CPU | Ordinary / final 8-client p95 |
| --- | ---: | ---: | ---: | ---: | ---: |
| Main | 1,773,568 | 1,761,280 | 1,753,088 | 0.111 / 0.258 s | 17.47–18.76 / 17.02–19.05 ms |
| HTTPX | 233,472 | 233,472 | 233,472 | 0.030 / 0.043 s | 17.12–18.88 / 18.26–19.88 ms |
| Wide | 8,519,680 | 8,339,456 | 8,200,192 | 0.707 / 1.539 s | 18.14–19.51 / 18.67–19.31 ms |

Wide repack wrote 6,637,681 versus 6,317,789 pack bytes, with median peak
RSS 59.4 versus 59.9 MiB. Eight-client throughput was approximately 1,054
versus 1,040 operations/s. The read samples are signed loopback requests
with a fresh server per trial. OS page-cache contents were not evicted.
The Go tests above bound the decoder cost separately. The trial supports
operator-selected low-churn cohorts with representative public or
authorized context, given the added rewrite CPU and the smaller main win.

The `packlocaldict` feature is recorded before publishing a new pack.
The decoder finds the context inside that pack, verifies the context and
the object's original SHA-256, and keeps both auxiliary records out of the
CAS locator. Format validation constrains record sizes and copy placement.
The previous reader refused admitted stores. The older explicit-reference
experiment is kept as a source patch and uses its own feature marker.
Portable snapshot transfer operates over verified logical chunks. The
physical feature remains gated for stores containing old pack generations.

Targeted tests cut a real child process before the preset pack rename and
after rename before directory fsync. Both stores reopened and deep verified;
retry and GC converged and exported a byte-identical snapshot bundle that
restored in a fresh store. Each separately corrupted context copy was masked
by the other copy. With both copies corrupt, the dependent member failed
while a separately uploaded object containing the context bytes stayed live.
