# Research verification checkpoint

The ordinary root package suite passed on detached source
, including subprocesses built
from that checkout. It used Go 1.27.4 on linux/amd64 with
. The command was  followed by the root test
binary with . The run exited successfully.

The test binary SHA-256 was .
The complete run log SHA-256 was .

The preceding ordinary run had failed on pending ZN7 base resolution,
a persistence simulator negative control, and concurrent manifest
lookup during a segment seal. The corrected cases are documented
with focused results and preserved failure evidence.

- [Authority proof and persistence control](../p9/results/authority-proof-control)
- [Concurrent readers and segment publication](../p9/results/live-segment-reader)
- [Archive codec and cross-format authority checks at c1a22bb](../c5/results/coreutils-codec-replay/integrated.log)
- [Executable laboratory range tests](../c5/results/executable-range-index/window-reuse)
- [Updated amd64 and emulated ARM64 quantizer checks](../c5/results/model-quantizer-portability)
- [Bounded retained-history resample](../c4/results/research-resample.json)

The 36-stage suite, hardware-scale qualification, and long-duration
pilot remain deferred. The Go runtime and compiler investigation
remains paused.
