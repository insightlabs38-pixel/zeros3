# ZeroS3 Z4 continuation

The [queue](QUEUE.json) tracks all 44 acceptance items. Its dependencies indicate
implementation or verification prerequisites.

- Item 1 merged in PR #117. Item 8's private archive and remote checksum verification
  are recorded in PR #119.
- PR #120 has the G5 memory change. The 256 MiB through 2 GiB checks passed at
  lower peak RSS. Concurrent migration and the full T1 gate are in progress.
- PR #121 merged matcher research evidence. Both variants reconstructed exact bytes,
  with performance regressions on several inputs. Item 4 remains incomplete.
- PR #122 holds the C2A contract and tested ZSR1 reader. Production index publication,
  writing, maintenance and rebuild are pending.
- PR #123 holds the tested T1 Git provenance failure control.
- In a resumed workspace, start with the current GitHub main and the draft branches.
  If /workspace/work/zeros3 is present, reuse its existing logs and corpora. The
  GOMAXPROCS=4 Go 1.27.4 candidate qualification uses p8r-t1-go1274-procs4.

The proposed ledger update is staged independently while #120 qualifies an immutable
fingerprint. Keep private incident logs in the archive repository.
