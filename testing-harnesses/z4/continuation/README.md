# ZeroS3 Z4 continuation

The [queue](QUEUE.json) tracks 44 acceptance items from the continuation brief. Its
dependencies indicate implementation or verification prerequisites. Each milestone
still uses its own qualification criteria.

- Item 1 merged in PR #117. The driver rejects failed children and partial results.
- Item 2 has exact 2 GiB single-store and concurrent memory evidence. Frozen
  PR #120 remains subject to its release qualification.
- Items 3–7 have measured results in the consolidated P8R milestone PR #127.
  Remaining corpus checks, regressions and qualification are tracked per item.
- Item 8 delivered and verified the append-only private archive. The runtime
  root cause remains open and the investigation is paused.
- Item 42 gained the merged PR #123 provenance and runner liveness fixes.
  The final integrated CI and conventional runner audit remains open.

At each milestone, record the tested source fingerprint and remote merge commit.
Record only the private incident archive's append path and commit in this repository.
