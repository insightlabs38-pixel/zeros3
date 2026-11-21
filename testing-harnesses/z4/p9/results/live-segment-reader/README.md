# Shared readers across a segment seal

The ordinary root suite at `2809e55` lost the manifest for
`b/live271234` during a concurrent shared open. A reader listed packs
before the writer published the sealed pack, then listed segments after
the writer removed its old name. A complete footer could also make a
still-unpublished segment disappear from the reader's locator.

Shared open and refresh now scan open prefixes first, then list sealed
packs, and install the locator in one operation. They retain a verified
segment with a complete footer while its final pack is unavailable.
Pack and segment names for the same verified file are only indexed once
when both are visible.

Two deterministic tests seal between inventories and open a reader
immediately before pack publication. The live-writer test now drives
25 independent opens against bounded concurrent write batches, rotates
multiple segments and checks all 3,000 acknowledged objects after
restart. The older unbounded fixture filled the shared temporary
filesystem during a diagnostic rerun. Its failure is preserved below.

The ordinary root suite here is separate from release qualification.
