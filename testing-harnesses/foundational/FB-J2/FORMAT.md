# Experimental grouped journal format

Only a freshly initialized store opts in with
`ZEROS3_JOURNAL_FORMAT=j2`. Its FORMAT.json requires `journalgroup`.
An existing v1 store ignores the initializer option. An older build
refuses the feature. The current reader dispatches by recorded feature,
checks the UUID in the file header, and refuses a missing grouped log.
An initialization interrupted before FORMAT.json may retry only with a
valid header-only file and the explicit option. An existing journal is
never overwritten by the new initializer.

Integer fields use big endian. The 48-byte file header has ZSG2,
version 2, two reserved zero bytes, the 36-byte store UUID, and CRC32C.
Its SHA-256 is the chain seed. Each group contains a checked begin
record, the original v1 records, then a footer.

| Part | Fields in order |
| --- | --- |
| 64-byte begin | J2BG, byte length, group number, first sequence, record count, previous digest, CRC32C |
| Frame region | Existing ZSJ1 frames with contiguous sequences and zero flags |
| 64-byte footer | J2FT, record count, physical start, group number, last sequence, SHA-256 |

The footer digest covers the begin, encoded frames, and the first 32
footer bytes. Group cap 32 MiB and 4,096 records. The current root queue
can refuse a batch exceeding the research cap. Both header geometry and
record boundaries are checked before any group record is applied.
The owner reuses at most 256 KiB of private encoding scratch. The writer
builds the whole group, does one offset write and one sync, then moves
its append cursor. Locks and namespace apply retain their existing
publication order. An I/O failure poisons the writer and reports
rollback status. Reads use verified namespace state.

A process or host crash under ordinary filesystem sync semantics can
leave any subset of the final group's bytes. A complete group may
replay even if its response was lost. Owners preserve and discard a
bounded incomplete final group only under this crash-only contract.
Recognizable later valid groups, evidence past a recovery budget, and
checkpoint contradictions refuse. Shared opens never truncate. An
existing raw-prefix checkpoint binding can adopt the namespace and
resume with the covered group's footer. Inspect and explicit salvage
recognize group boundaries and preserve fault evidence.

Loss of the final acknowledged group after a successful sync can have
the same bytes as an interrupted unacknowledged append. Without an
independent durable witness this is **UNDETECTABLE**. The format has
no new authoritative sidecar and leaves hardware and media qualification
open. Existing journal files and old-store defaults stay in place.
There is no in-place migration, downgrade, or history retirement.
Portable snapshots are available for copying captured namespaces and
retain their own scope. Keep the original store and its history when
attempting any cross-format transfer.
