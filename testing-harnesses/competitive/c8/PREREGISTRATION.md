# C8 · Stream verified reconstruction

C3 and C5 measured multi-second receiver CPU for an approximately 160 KiB
TypeScript closure. The receiver first proves each new record against its
SHA-256 and then reads the target again to verify the CDC layout and ETag.
The client also stages the signed closure in a temporary file. Test whether
a bounded stream can feed target, chunk and ETag verification together, and
whether small signed transfers can avoid scratch I/O.

Compare frozen C7 with C8 on identical warm authority sources and receiver
knowledge. Use independent compressed TypeScript, producer SST/Parquet,
a generic shift and unrelated data. Track exact response and commit
behavior, elapsed time, process CPU/RSS/I/O, HTTP bytes and scratch writes.
Exercise no-change retries, malformed manifests, and the established
actual process-cut matrix. Preserve ordinary S3 and archive finalization.

Profile planning scratch separately under canonical and splice analysis.
Compare foreground S3 during bounded transfer and maintenance, using an
unloaded control. Leave a broad scheduler or KMS change outside the scope
of these measured improvements. Reuse the existing frame and journal.
