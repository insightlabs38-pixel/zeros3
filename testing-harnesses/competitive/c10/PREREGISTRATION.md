# C10 · Exact compressed read paths

Reproduce TypeScript whole and sparse reads against C9 after the same
four-stage physical finalization. The primary question is whether
independent seek restarts or predictor work dominate exact output replay.
Compare whole-object sequential replay with the incumbent split seek
schedule on identical stores. If this leaves a CPU bottleneck, profile
the exact predictive decoder and test a bounded algorithmic change.
Preserve all original bytes, range checks, cancellation and digest proofs.

Test TypeScript alongside RubyGems, PyArrow Parquet, RocksDB SST and an
entropy control. Hold out HTTPX until after selecting mechanisms.
Run finalized CDC, canonical and deep lanes with source hashes and
allocated bytes recorded independently. Alternate binary order and use
one and four clients. Report 4 KiB, 64 KiB, 1 MiB and full-object times,
CPU, process reads, RSS, emitted bytes and application SHA-256.
A first process pass still inherits the host's Linux page cache.

Treat a large whole-read gain at comparable storage as the primary
gate. Accept a specialized path if it has an explicit useful range
and no correctness loss. Charge any checkpoint/materialization bytes.
Use focused tests for wrapper fidelity, corruption, bounded memory,
range behavior, concurrent reads and fallback.
