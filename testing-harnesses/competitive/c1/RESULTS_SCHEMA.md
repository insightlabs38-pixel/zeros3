# C1 result records

Each JSONL line is an observation. Seconds are elapsed monotonic wall
time. Bytes are integers. The environment and corpus files are JSON.
Raw error records remain in the evidence and are excluded from rollups.

- S3 request rows identify system, repetition, concurrency and operation.
  Successful latency quantiles are in milliseconds. Logical bytes count
  successful application payloads. The service CPU and I/O counters are
  Linux process deltas, and RSS peak is the process-to-date high-water mark.
  Allocation includes directory and file blocks in the store only.
- History rows identify system, repetition, family, profile and phase.
  Backup rows track repository and client cache separately. Restore rows
  appear after all requested files pass SHA-256 in that revision. Report
  allocation after equivalent full-history maintenance where available.
  Reclaim is asynchronous or absent in some backends, so a delete row
  can report a different phase from a prune or reclaim row.
- Xet rows separate bucket and dataset operations. Upload verification
  denotes API success. A verified download has a SHA-256 field. Revision
  is an immutable Git commit ID. Interface deltas are aggregate link-layer
  observations on an isolated interval and may include background traffic.
  Hosted backend allocation, request counts and application retries are
  unmeasured unless separately instrumented.
- Request rows with invalid status do not provide completed quantiles.
  Missing metrics are unmeasured, and null p99 means too few observations.
  Retain the settings and replay commands alongside these results.
