# FB-07 · Physical integrity and targeted repair

**In progress.** `zeros3 scrub` verifies every sealed pack body
and each indexed record, including dead data and duplicate copies.
It shares the publication verifier and can continue through
individual record failures while the validated layout still
identifies the remaining record boundaries.

Reports identify the tier, pack ID and failure class. Record
findings include ordinal, expected digest, logical length and
payload offset. The report counts all observed issues and retains
the first 256 diagnostic records. The optional physical byte
budget returns a resumable cursor and the next pack's size.

In the focused fixture, ordinary reads recovered through a
healthy copy. Scrub still found a bad duplicate payload, a bad
unreferenced header and payload, and the pack body mismatch.
It also rejected a damaged footer and preserved exact coverage
across a one-pack budget and resume.

Offline `repair -scrub-report` restores indexed raw records.
It rechecks that each finding matches the active pack index,
reuses a healthy local copy where available and requests only
unavailable record digests from a configured signed peer. The
sealed replacement must match the original pack SHA-256 exactly.
The pack identity, journal and object metadata remain intact.

The loopback fixture repaired three damaged records using a
local duplicate, a header reconstructed from its index and one
signed peer GET of 9,216 bytes. A process cut before rename left
the damaged predecessor byte-identical and the next run rebuilt
the original file. Compressed physical recoding and carry hash
accounting remain in progress.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
