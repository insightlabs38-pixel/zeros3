# Closure retry after an incomplete transfer

A production export carried a 17 MiB pinned object. The import read all
component bytes and reached a durable CAS flush before the test removed
the stream trailer. The pre-fix receiver left 32 staged CAS files.
The fixed receiver aborts the active batch, publishes no authority
records and reopens successfully. A retry reuses durable components and
verifies the original SHA-256 through production reconstruction.
The existing full and thin transfer interruption tests also passed.

The second set of focused tests verifies version, format, depth,
record-form and IQ3 feature negotiation. The receiver refuses
out-of-order records and an absent thin base before staging data.
Valid full and thin transfers, corruption refusal and reopen retries
still pass with the same closure frame.

A negotiated export derives requirements from the actual records it sends.
A peer with the baseline pin and delta forms receives the same version-1
bytes as the existing unnegotiated exporter. The seeded IQ3_S and strict
IQ3_S derivations refused a peer lacking their quantizer feature before
writing any frame bytes. Both imported and reconstructed the original
SHA-256 when that feature was advertised.
