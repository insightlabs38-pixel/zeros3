# ZeroS3 Z4 continuation

The [queue](QUEUE.json) tracks 44 acceptance items from the continuation brief. Its
dependencies indicate implementation or verification prerequisites. Each milestone
still uses its own qualification criteria.

- Item 1 merged in PR #117. The driver rejects failed children and partial results.
- Item 2 reproduced the 256 MiB migration RSS regression and isolated decoded windows
  and packed reads in the peak heap. Candidate comparisons are underway.
- Item 8 reproduced the heap failure on unmodified P8 with Go 1.27.0. Control runs
  and the private evidence delivery are in progress.
- Item 3 has a pinned Git-pack smoke result. The paired benchmark and remaining
  corpus families are pending.

At each milestone, record the tested source fingerprint and remote merge commit.
Record only the private incident archive's append path and commit in this repository.
