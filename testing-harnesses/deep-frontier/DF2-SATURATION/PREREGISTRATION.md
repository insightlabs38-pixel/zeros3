# Concurrent replay saturation

Pin DF1 at `752ffd5f426509fe3d93a1d7c88c7e4df5bcc3e2`. The
curl store occupies 4,616,192 B. The existing signed 240-request
mix uses 60 whole GETs, 80 small ranges, 60 medium ranges,
20 HEADs and 20 PUTs per order. DF1 measured whole p95
163–189 ms at two clients, 461–462 ms at eight and 1.93–1.96 s
at 32. A fresh replay gives 144 ms, 455–580 ms and 1.96 s.
Allocation-free validation, fused emission and deferred refill are
already retained. The wider per-field writer was rejected.
DF1 observed load and maintenance. It tested no request admission
or sharing of native translated replay across HTTP requests.

Question: can we reduce queueing or duplicated work at 8–32
concurrent native readers on two cores with fixed physical storage
and a bounded transient budget?

- CPU saturation predicts nearly flat successful throughput across
  client counts, roughly constant CPU per GET, and output/input
  replay dominating samples. Higher concurrency chiefly adds delay.
- Shared locks predict a rising blocked fraction, with engine
  synchronization in the critical path and idle capacity.
- Excess runnable replay predicts growing scheduler delay and
  throttling. Limiting admission will improve internal latency or
  transient memory if runnable work is the problem, even when
  external queueing preserves the completion-time bound.
- Memory or I/O predicts rising record bytes, GC overhead or read
  wait per request, and materially different warm and evicted runs.

Measure signed GET-only first at 2/8/32 clients, then a distinct
signed writer arm. Include hot repeated and diverse objects,
process CPU and cgroup counters, Go scheduler/mutex/GC profiles,
physical record reads, exact bodies, live heap and RSS. Main warm
tests use two opposing orders and at least 300 whole GETs per lane.
Use absolute ranges across trials and preserve per-operation samples.

Investigate bounded admission and, if hot duplicate replay is
substantial, coalesce overlapping identical authorized requests.
Keep a cap of two producer slots and 16 MiB of shared output with
no persisted data. Bound waiters and make cancellation observable.
Profile counters separately when they can perturb timing. Accept
only with unchanged physical allocation, a repeatable signed gain
under saturation and practical low-concurrency cost. Report distinct
work, cold behavior, transient heap, cancel and failure tests.
Keep all ordinary behavior and serialization defaults. Use only curl
and other registered development inputs to choose methods. Open the
nginx confirmation bytes after freezing the selected implementation.
