# C11 · Foreground isolation

Freeze the C10 binary and reuse the C3/C8 TypeScript source identities and
the receiver with a verified differently named authority base. Add a
deterministic cohort with mixed live and retired records in the same packs
and keep one shared immutable seed for each receiver trial.

Issue an equal mix of GET, range GET, HEAD, LIST and PUT requests at 1, 4,
8 and 16 clients. Use the same logical work, alternating lane order.
Measure idle and incumbent first, then a fixed-sleep throttle and a
foreground-aware policy. Separate import, GC/repack, restore and mixed
work. Check that indexed analysis and offline archive finalization still
honor the writer lease. Compare at two CPUs and repeat the winning or
negative high-contention cases with one Go worker as a resource control.

Use cancellation-aware, bounded waits before expensive background work,
away from commit locks and durable publication. Keep the existing
foreground request counter and safe maintenance epochs. At every
background checkpoint allow progress even on sustained foreground traffic.
Do not publish results without the existing digest and closure proofs.

Record foreground p50/p95/p99, overlap, throughput, background elapsed,
import HTTP bytes and object identity, reclaimed bytes, peak RSS, CPU,
cgroup throttling and process I/O. Read the online maintenance mark,
scan and reader/writer grace timings before attributing the bottleneck.
Collect a separate runtime trace batch if symptoms require it.
Keep the policy opt-in unless paired low-load and mixed measurements
justify a stronger rollout. A latency reduction through excessive
background delay does not pass. Require unchanged exact reads, deep
verification and progress under sustained load, including cancellation
and safe reopen of the destination.
