# Z3-43 LLVM bitcode

Verdict: **DEFER** semantic normalization; **KILL** generic structural/grouped
byte transform on this corpus. Production untouched; no new kind, wire or default.
The observed ID churn is real, but no exact identity normalization is defensible
without substantially more IR semantics. Diagnostic name matching is not admission.

Corpus: 65 native bitcodes / 56 unique SHA256s / 15,502,760 input bytes:
five pinned hiredis releases v1.0.0–v1.4.0 × hiredis/read/sds C units × O0/O2;
three pinned SQLite amalgamations 3.45.3, 3.46.1, 3.49.1 × O2;
eight controlled C/C++ states × O0/O2 (base, constant, early/late function,
reorder, type insertion, rename, restored base). Unchanged sds releases and four
restored controls are deliberately retained, not counted as additional unique data.
These are real release-source compiled histories, not published bitcode histories.

Producer/oracle: cached Debian clang/LLVM 19.1.7 (3+b1), fixed x86_64 GNU target,
g0/no-ident, retained value names; SQLite thread/load-extension/memory-status
options are pinned. File basenames stay fixed across releases. Controlled sources
are shell-copied to input.c/input.cc so filename churn does not contaminate edits.
Source commits, archive/member SHA256s, compiler arguments/cwd and output/IR/dump
hashes are recorded. No downloads or compiler are needed for recipe decoding.

Mechanism: strict generic bitstream framing (blocks, record codes, inherited/local
abbreviations, Fixed/VBR/Char6, arrays/blobs, lengths and zero alignment).
Unknown LLVM block and record IDs are preserved exact without IR interpretation.
Unsupported envelopes, malformed framing or bounds reject; source CDC remains
authoritative. Functions, leaf blocks and grouped record byte cuts reuse Concat.
Record boundaries may be bit-aligned: cuts round down to byte boundaries, leaving
boundary bits in exact adjacent bytes. No bit normalization or hidden patch table.
Existing pricing/range/region routines were mechanically copied/extracted from
Q14. Research kernel: 11 functions/methods; production LOC/helper growth is zero.

Limits: source 128 MiB, depth 32, 262,144 blocks, two million records,
65,536 abbreviations / one million abbreviation operands, blob/region 16 MiB,
decoded values min(eight million, 32 × source bytes). Array/blob declared work is
checked before expansion, including zero-width Fixed arrays. Decoder uses existing
bounded Concat/Stored primitives; the encoder retains a whole-file diagnostic plan.

LLVM's independent analyzer agrees on every per-block instance/record count:
28,242 blocks and 1,854,143 records over all 65 outputs. Numeric operand hashes
also match LLVM for 1,853,891 records; 252 native blob records are excluded from
that numeric comparison and retain full byte proofs. Native IR names annotate
function fingerprints only. Equal shapes do not prove equal semantics or IDs.
SQLite transition 3.45.3→3.46.1: 1,465 common names, 1,260 same record shapes,
but only two byte/operand-identical function bodies. Next transition also has
only two identical bodies. Hiredis hiredis.c/O2 has zero identical common bodies
in the first two transitions, then 35/46 and 43/46 in later transitions.
Controlled early insertion, reordering and type insertion change same-named
operands; late insertion preserves two existing bodies. No pure abbreviation-only
aggregate match was observed. These diagnostics cannot distinguish every constant,
type/value ID and metadata change; no unsafe global renumbering is implemented.

Packed cumulative bytes include roots, full reachable closure and 64 B/object:

| Cohort | Source CDC | 4 KiB CDC | Functions | Leaves | Grouped records |
|---|---:|---:|---:|---:|---:|
| Hiredis, six histories | 1,151,713 | 1,201,747 | 1,297,894 | 1,420,514 | 1,596,298 |
| SQLite, three releases | 10,324,293 | 11,693,857 | 12,153,404 | 13,879,324 | 14,970,002 |
| Four controlled histories | 62,587 | 65,383 | 71,904 | 80,019 | 93,259 |

Across cohorts functions cost 13,523,202 versus CDC 11,538,593 (+17.2%).
First-sight sums: CDC 3,617,099 / functions 4,326,441 / records 5,608,626 B.
CDC + function union for SQLite is 22,477,697 B. All additive layouts lose.
Strongest positive: sds/O0 functions 74,005 vs CDC 79,917 B (−7.4%), but fine CDC
is already 69,493 B and additive union is 153,922 B. Restored controls add zero.

325 serialized full exact proofs, 975 fresh 4 KiB ranges; every read lies in the
declared closure. Worst logical Source API amplification: CDC 51.21x, fine CDC
14.65x, functions 48.58x, leaves 116.23x, grouped records 164.14x. These are not
physical CAS I/O figures; whole-object SHA256 proof is separate from range reads.
Final harness 4.422 s / 291,048 KiB RSS (284.2 MiB), compiler excluded, includes
whole-file plans, all accounting/proofs/ranges; no streaming decoder memory claim.

Validation: complete analysis tests/vet pass; native tiny fixture, unknown IDs,
malformed abbreviations, truncation and declared zero-width expansion are covered.
Final fuzz 3,103 executions / 21.023 s PASS; source mismatch/missing inputs reject.
No concurrency changes or production diff warrant additional broad race/blackbox.
Evidence: analysis `testdata/q15-bitcode` pins, source/producer provenance, native
comparisons, Stage-A and final compressed costs/resources; `bitcodez3/testdata`
contains original controlled native bytes. No production patch or draft required.

Reopen only with a bounded semantic identity/replay mechanism that survives real
value/type/table churn and beats the retained-source union, without becoming a compiler.
