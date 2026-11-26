# FB-07 · Physical integrity and targeted repair

**Partial.** `zeros3 scrub` verifies every sealed pack body
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

Offline `repair -scrub-report` restores indexed raw and independent
DEFLATE records.
It rechecks that each finding matches the active pack index,
reuses a healthy local copy where available and requests only
unavailable record digests from a configured signed peer. The
sealed replacement must match the original pack SHA-256 exactly.
The pack identity, journal and object metadata remain intact.

The loopback fixture repaired three damaged records using a
local duplicate, a header reconstructed from its index and one
signed peer GET of 9,216 bytes. A process cut before rename left
the damaged predecessor byte-identical and the next run rebuilt
the original file. A deterministic DEFLATE recode restored the
263-byte original pack using a healthy raw copy of its 14,848
logical bytes.

`repack -compression off -verified-raw-carry` is an opt-in
two-pass raw carry. The old record header binds the candidate
to its authoritative index. SHA-256 covers the payload in the
writer and again in the private sealed replacement. The file's
identity, size, modification time and exact index are checked
through publication. The 8,192-byte fixture counted 8,192 bytes
in each of these passes. A staged-file change left the source
intact. A compressed source fell back to the ordinary verified
reader. Final reads after reopen matched the original object.

Compressed carry retains its established proof path. Block-table
and preset record repair, an advisory scrub schedule, and common
taxonomy across journal and metadata faults remain open.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
