# Quantizer portability and authoritative model ranges

Both emulated ARM64 CPU settings consumed the same fixed IEEE-754 input file as amd64. Of 296 profile/input pairs twelve differed on each ARM64 setting, covering the same six inputs with two arithmetic profiles, limited to nonfinite input and extreme IQ4_NL scale output. The real Pythia excerpt and IQ3_S vectors matched. An independent GCC 14.2 llama.cpp b3991 strict oracle matched the original vectors. Its fused build differed in two Q2_K blocks in one weighted heavy-tail vector. These observations apply to the pinned compiler and emulated CPU profiles. They do not establish a native ARM producer matrix.

The production encoder writes exact explicit bytes for windows with nonfinite input values, nonfinite importance weights or nonfinite IQ4_NL derived scales. The new fallback uses the existing raw-window recipe. Independent amd64 and emulated ARM64 regressions verified byte-identical fallback alongside ordinary derived windows. Existing authenticated windows retain their read path.

Small S3 ranges on existing ZN7 authority now use only the overlapping authenticated windows. In paired runs on the published IQ3_M file, the median first 4 KiB range dropped from 152.4 ms to 74.7 ms. The repeated median went from 96.8 ms to 82.9 ms. All readbacks and full-object digests matched. The CDC control and whole-read timings varied materially under concurrent load, so this is a range result. The change retains the existing sequential read path.

After cross-format tensor matching was integrated at `c1a22bb`, the
current amd64 build passed exact Q4_K, IQ3_S and IQ4_XS authority
derivation, old-reader refusal and nonportable-window fallback. The same
fallback test passed on current emulated ARM64 with both CPU profiles.
The earlier cross-toolchain divergences still delimit the producer
profiles supported without an explicit-byte fallback.
