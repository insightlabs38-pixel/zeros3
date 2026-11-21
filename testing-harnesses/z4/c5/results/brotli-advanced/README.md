# Brotli q10 and q11 exact residual trial

The baseline work uses data from the pinned C1 generated table history. Archived PyArrow 26.0.1 q8 pages and fresh q10/q11 files preserve the original row groups. PyArrow independently verified the decoded tables, and each tested page came from a hash-pinned file.

The runner selected eight stratified 32 KiB to 1 MiB page bodies per file. It compared fixed-basis Go q9 with two Python Brotli reference versions and both one-shot and 64 KiB flush modes. All 312 encoded-stream comparisons reconstructed exact bytes. For original Arrow q10 and q11 pages, the q9 window-delta residual used 547,064 versus 545,864 opaque bytes and 547,065 versus 545,865 opaque bytes respectively. Reference q10 and q11 similarly failed to reduce the opaque payload. Reconstructing the Go q9 predictor adds CPU. This test does not establish the performance of a native deterministic q10 or q11 encoder.
