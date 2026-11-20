# Closure retry after an incomplete transfer

A production export carried a 17 MiB pinned object. The import read all
component bytes and reached a durable CAS flush before the test removed
the stream trailer. The pre-fix receiver left 32 staged CAS files.
The fixed receiver aborts the active batch, publishes no authority
records and reopens successfully. A retry reuses durable components and
verifies the original SHA-256 through production reconstruction.
The existing full and thin transfer interruption tests also passed.
