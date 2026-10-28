# Z3-43 LLVM bitcode — checkpoint

Q15 remains active; no promotion decision yet. Production untouched.

Strict generic bitstream framing accepts unknown LLVM block/record IDs and
preserves every target byte. Nested block lengths, abbreviations, VBRs, arrays,
blobs and alignment are bounded. No compiler or LLVM dependency at decode.
Structural function/leaf-block baselines and grouped exact byte cuts use existing
Concat. Operand hashes are diagnostic only; they do not establish semantic IDs.

65 native bitcodes: five pinned hiredis release sources × three C translation
units × O0/O2, three pinned SQLite amalgamation releases × O2, eight controlled
C and C++ edits × O0/O2. These are compiled source histories, not published
bitcode-artifact histories. Cached Debian clang/LLVM 19.1.7 supplies producer,
IR-name diagnostics and independent analyzer counts. Source filenames are stable
within histories; controlled sources are mechanically copied to input.c/input.cc.

Native analyzer parity: 28,242 blocks / 1,854,143 records across all 65 outputs.
All 325 representation cases reconstruct exact serialized bytes and cold ranges.
SQLite cumulative CDC 10,324,293 B versus function grouping 12,153,404 B;
CDC + function grouping 22,477,697 B. Generic grouping is not a storage win.
Hiredis sds/O0 is a narrow standalone positive, requiring aggregate assessment.

Same-named SQLite functions show strong operand churn: only two bodies remain
byte-identical in each release transition despite many identical record shapes.
Controlled early-function/type insertion and reordering also change operands.
No unsafe value/type ID renumbering has been attempted. Work-bound fuzzing and
final proof/cost/churn validation remain in progress; see q15-bitcode artifacts.
