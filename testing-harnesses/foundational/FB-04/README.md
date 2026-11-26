# FB-04 · Shared namespace mutation order

**In progress.** The object-root queue batches PUT and DELETE. A
DeleteObjects request submits all eligible keys together while retaining
the request's validation, response order, quiet handling, and per-key
errors. Duplicate and missing keys do not add journal frames.

The group prepares object and history overlays before the append.
Readers can see the prior coherent view during the sync. Queue waiters
only receive a result after the leader has applied or failed the group.
If the flush fails, results derived from the provisional overlay do
not report success. A panic before successful apply poisons the journal
and fails waiters before releasing them.

The focused real signed request test checked 1,000 existing keys with
one sync in both v1 and J2. An exact foreground read completed while
the sync was held, and every deletion survived reopen. The grouped
ENOSPC test preserved the old roots and exact log.

Bucket creation/deletion, upload creation/part/abort/completion,
and history-prune frames share the queue in order. They use separate
batch boundaries because their provisional state differs from the
object-root overlay. Multipart ingest and checksum work stays outside
the queue. All journal writes in these mutation paths release the
namespace lock while the commit lock excludes writers. Focused probes
checked each path with both formats, including exact multipart bytes.

Three interleaved overlay trials of the signed 1,000-key delete
measured 228.9 ms before and 10.3 ms after, excluding fixture setup.
The queue passed an independent interval-order model for concurrent
PUT, conditional create, DELETE and read at 32 histories per format.
The real process-kill rerun and broader focused race logs are in
[results](results/). Paired foreground latency measurements remain
in progress.
