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

Targeted peer repair and carry hash accounting are in progress.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
