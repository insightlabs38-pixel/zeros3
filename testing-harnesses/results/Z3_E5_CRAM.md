# E5 CRAM

Verdict: **KILL** a CRAM-specific framing transform on measured evidence;
**DEFER** semantic/entropy replay. Production untouched. No CRAM parser, codec,
reference/base primitive or external dependency is added to candidate decoding.
Generic fine CDC already beats the native block oracle in every generated profile.

Corpus: pinned HTSlib 1.21 commit 0f8bedc11e3ee039060a058b5cd5ec99e0b593a2,
four public CRAM regression fixtures, three FASTA references and four companion
SAM files (including the 1,000-read producer seed). Native PySAM 0.23.3 /
samtools+HTSlib 1.21 expands the seed to 32,000 reads and produces 18 workloads:
CRAM 3.0, 3.1 and 3.1 `no_ref=1`, each with base/base-cell/quality-cell/append/
metadata/slice-repack states. These are scaled controls, not natural histories.
32 copies preserve pairing/coordinates/sequence/quality/tags, with unique names;
MD/NM are explicitly reference-derived, not promised as original SAM text.

All 576,768 generated alignment-field comparisons pass. Ten public paired-SAM
alignments also match. range.cram's 112 alignments have native decode evidence
only, without an original SAM. The Java aux fixture is an explicit negative:
two reads exhibit seven H→B:signed-byte and unsigned→signed array-subtype
conversions. Underlying bytes match, but original SAM types/numeric semantics
do not; these are never counted as semantic PASS or normalized away.
The public xx fixture's reference-length warning (30 versus 20) is retained;
its original-SAM field checks still pass. Missing indexes do not affect full scans.

The native API oracle (separate small C helper linked to pinned HTSlib) locates
2,628 encoded blocks. Each native payload must equal its source span; every
observed CRAM 3.x block CRC32 is independently verified. Methods observed:
RAW, gzip, rANS 4x8, rANS Nx16 and name tokenization. Header/unknown bytes stay
opaque. Native code never participates in candidate representation reconstruction.
No candidate parser is justified when its ideal native boundary oracle loses.

25 inputs / 25 SHA256s include 22 CRAMs plus three reference artifacts. Prices
include packed objects, full closure, roots and 64 B/object:

| Six-state native profile | Source CDC | Fine CDC | Native blocks | CDC + blocks |
|---|---:|---:|---:|---:|
| CRAM 3.0 | 487,391 | 311,633 | 331,411 | 818,802 |
| CRAM 3.1 | 498,114 | 325,454 | 344,104 | 842,218 |
| CRAM 3.1 no-ref | 545,142 | 393,643 | 404,052 | 949,194 |
| Four public fixtures | 29,309 | 30,821 | 48,921 | 78,230 |

Strongest positive: native blocks save 32.0% standalone in the 3.0 controls.
Fine CDC saves more (36.1%) without understanding CRAM. Payload-only grouping
costs 373,798/387,287/448,576 B: extra framing/root overhead outweighs separation.
First sight 3.0 CDC 114,435 / fine 120,852 / native 123,126 B; structured grouping
has an initial tax. Native reference compression is effective: first-sight decoded
SAM oracle costs 325,875 B, almost three times encoded CRAM CDC.
Six-state SAM oracles cost 397,185/397,185/337,786 B, but omit exact entropy,
container/header decisions and never claim exact outer CRAM reconstruction.

FASTA artifacts cost 325,141 B under source CDC, priced separately rather than
hidden in a supposed free base. Native semantic reads use matching format-local
references where required; retrieving exact encoded CRAM bytes needs none.
No generic BASE or filesystem lookup enters a representation. This research
makes no exact CRAM-from-SAM re-encoding or reference-resolution claim.

Existing Concat/Stored primitives supply 138 serialized full byte proofs and
282 fresh 4 KiB ranges; every read is inside its declared closure. One complete
SAM oracle is proved per logical row; no semantic failure becomes a byte-proof
success claim. Whole reconstruction retains SHA256, length and byte equality.
3.0 max logical Source API amplification: CDC 46.80x, fine 2.81x, native blocks
8.20x; not physical CAS I/O. Full-source SHA proof is separate from partial reads.

Final harness: 3.245 s / 139,724 KiB RSS (136.4 MiB);
includes whole inputs, SAM views, exact proofs, accounting/ranges; compiler,
native producer and scan helper are excluded. No streaming production decoder
claim. Complete analysis tests/vet pass; source mismatch/overlapping spans reject.
Native payload/CRC assertions and the specific seven semantic mismatches are
strictly checked. No new decoder/concurrency path warrants fuzz/race churn.

Mechanism reuse: shell copy/extraction of Q13's pricing/range harness and the
native signature helper, with only boundary/variant adaptation. Source ≤16 MiB,
SAM oracle ≤16 MiB, native blocks ≤16 MiB, depth four. Public archive/member and
wheel hashes, producer/native-library/source hashes, versions, inputs, native
negative, packed costs and resource records are durable in `testdata/e5-cram`.

Reopen with related natural CRAM histories and a bounded exact decision mechanism
that beats fine CDC and the retained-source union. Current evidence supports
neither a new codec family nor a production integration; the useful lane is closed.
