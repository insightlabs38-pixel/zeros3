# Research verification checkpoint

The ordinary root package suite passed on detached source
`d89324758500a6776263c8ba463c2486ba16e3b1`, including subprocesses built
from that checkout. It used `go1.27.4 linux/amd64` with
`GOMAXPROCS=2`. The command was `go test -c` followed by the root test
binary with `-test.timeout=30m`. The run exited successfully.

The test binary SHA-256 was `cc136e102d510f7838babfb2360dbbed5f3819c6cc38e4929b3d3953e3c90337`.
The complete run log SHA-256 was `bb54d04cbdbe21a70811f7fa6bac3dc9eea13ae14f7c5c2cf09b8ac8c9f81f8b`.

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
