# C9 · Integrated consumer workflows

Freeze the C8 head at `8f1eb34a626dc1e89bcf8232733421124bb22ee9`.
Use the C5 TypeScript release history and independent producer Parquet/SST
fixtures. Build a minimal artifact mirror with boto3 and a database snapshot
workflow that actually opens restored data using PyArrow and RocksDB.

Exercise initial/revision/exact uploads, conditional failure, listing,
range and full reads, deletion, bundle retention/restore and restart. Compare
ordinary S3, native CDC and scoped structural transfer on the same archive
payload at a reproducible local pace. Keep finalization, active S3 latency,
replica CPU and retained allocation separate. Compare ordinary request rates
with MinIO and RustFS, snapshots with Restic, and exact/matched shifts with
the pinned local OpenXet pair. Hosted testing stays outside this run.

Verify each stored/restored object, the selected application metadata and
one actual post-ack process interruption. A win may remain specialized if
archive full-read replay or foreground tail costs constrain deployment.
Retain research maturity, the single writer boundary and existing formats.
