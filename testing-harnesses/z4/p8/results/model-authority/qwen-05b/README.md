# Qwen2.5-0.5B production migration control

The pinned base and instruct safetensors are 988,097,824 bytes each.
The fixture streamed both source files into a CDC store and verified the
published B5 SHA-256 pins. The consolidated P8R production binary used
Go 1.27.0, one migration worker and a 160 MiB soft Go heap limit.

The encoder kept one body in CDC and admitted the other as a depth-1 ZN7
container. Migration used 147.98 CPU seconds and peaked at 119,248 KiB RSS.
Allocated store bytes moved from 1,980,801,024 to 1,457,487,872 bytes after
GC, repack, compact and GC. A fresh production deep verification passed.
A separate process streamed both bodies and matched both fixture hashes.

This is a single large-model control. Its measured RSS is from the
migration process. The fixture and verification resource files report
their own distinct peaks.

The optional fixture and readback use the same test entry point with
`go test -tags p8qwenexperiment -run '^TestP8QwenControl$'`. Set
`ZEROS3_P8_QWEN_DIR` to the directory containing the pinned inputs,
`ZEROS3_P8_QWEN_STORE` to the dedicated store, and
`ZEROS3_P8_QWEN_CHECK=1` for the readback phase.
