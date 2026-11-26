# ZeroS3 C12 · Format-neutral delta programs

**C12_SPECIALIZED after native follow-up.** Existing generic authority deltas retained large
savings on several neutral histories. Aligned residuals and page anchors
improved selected encode costs. The best prototype still traded slower
range reads for small physical savings after counting its source base.

## Finalized production histories

Nine development histories and an independent HTTPX tar history were
stored with C11. Both lanes ran GC, full recompression, compact and final
GC. Exact signed S3 reads and deep verification reproduced every input.

| History | CDC allocated bytes | Generic graph allocated bytes |
| --- | ---: | ---: |
| TypeScript tar | 9,269,248 | 4,980,736 |
| SQLite | 5,079,040 | 2,256,896 |
| Sparse volume | 9,359,360 | 3,502,080 |
| Event log | 290,816 | 196,608 |
| HTTPX tar, held out | 372,736 | 262,144 |
| Independent entropy | 12,689,408 | 12,689,408 |

The original TypeScript gzip files also kept 12,689,408 bytes under
generic analysis. Their canonical-wrapper path remains the separate C10
result. On the volume, generic whole p50 improved 33.2 → 20.9 ms and
4 KiB p99 rose 2.9 → 8.6 ms in the single-client first-pass probe.

## One-base programs

The table shows allocated KiB for the isolated patch file. Each patch
can be added to the same verified exact base. Prototype payloads include
frame digests and the seek index. External rows use whole restore.

| Pair | XD | Aligned XOR | Packed | Anchored XOR | xdelta3 | bsdiff | zstd |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Volume 0 → 1 | 32 | 16 | 20 | 16 | 8 | 4 | 20 |
| Volume 1 → 2 | 32 | 1,528 | 20 | 28 | 8 | 4 | 20 |
| Shift 0 → 1 | 12 | 3,596 | 12 | 12 | 12 | 12 | 12 |
| TypeScript tar 1 → 2 | 236 | 4,636 | 228 | 4,628 | 84 | — | 104 |
| HTTPX tar 0 → 1 | 4 | 48 | 4 | 48 | 4 | 4 | 4 |

**RESEARCH_ONLY** for aligned and anchored XOR. On the shifted volume,
anchors recovered from the aligned residual's 1,528 KiB patch. The
anchored patch plus base used 3,235,840 bytes versus 3,239,936 for XD.
In the synthetic shift pair, anchored encoding consumed 21 ms CPU versus
110 ms for XD at the same allocated patch size. Its isolated read p99 was
270 µs versus 30 µs. The benchmark records the complete latency,
allocation and per-GiB CPU curves. Large layout changes and held-out
HTTPX gave XOR little headroom.

**KILL** for the packed control/address/literal split at these frame
sizes. It cut some patch sizes, including the volume and one TypeScript
tar pair. It increased encode CPU on both cases and frequently increased
sparse verification work. The strongest external encoders also beat its
patch sizes on those pairs. BSDiff's 4 KiB volume allocation required
about 1.37 s encode CPU, versus 0.10 s for XD.

## Validation and limits

The final matrix has 131 exact fresh-process decodes and 9,728 exact
4 KiB seekable reads. Focused tests cover truncated frames, corrupt
payloads, wrong source bytes, shifted residual recovery and invalid
ranges. The first xdelta3 gzip trial caught wrapper regeneration.
The byte-exact baseline disables external decompression and recompression.

The isolated timings use an accessible raw base and an ambient page
cache. Production CAS already verifies components. The lab's independent
frame hashes give its decoder a different verification cost.
The original framed residual and packed stream formats remain research
results. The native follow-up integrates two opt-in apply policies without
changing the priced native envelope, finalized bytes, or read format.

Retaining priced envelopes at a 4 MiB payload ceiling reduced apply time
from 1.53 s to 0.25 s on the 20-revision pressure test and from 0.67 s to
0.10 s on the windowed edit. The cache reused 18 of 19 selected pressure
plans, up from five with first-come admission. Exact small-edit matching
also saved work independently. See [native feasibility](FOLLOWUP.md).

[Reproduction](REPRODUCE.md) · [Fixtures](fixtures.json) · [Raw results](results/)
