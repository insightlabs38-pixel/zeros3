# Concurrent 2 GiB G5 memory checks

The two concurrent migrations in each cohort used independent copies of the
same G5 fixture, with three 2 GiB versions per store. The P8 binary was built
at af9b39db08db. The candidate is the pre-freeze development build for the
memory implementation recorded in 50490fd5b714. Its embedded VCS metadata
identifies the dirty worktree based on 4ede37c842cd. The separate T1 gate uses
the frozen #120 Git head. See build-identity.log for the measured executables.
Both binaries used Go 1.27.0. Each process migrated with
`authority migrate -apply -max-depth 2 -archive`.

| Cohort | Aggregate sampled RSS | Process peak RSS | Combined CPU |
| --- | ---: | ---: | ---: |
| P8 baseline | 416,500 KiB | 210,812 and 217,364 KiB | 599.42 s |
| #120 candidate | 322,900 KiB | 163,944 and 165,012 KiB | 435.11 s |

Aggregate RSS is sampled during simultaneous migration. Per-process peak
comes from wait4. Other host jobs changed between cohorts, so these CPU
and wall figures are descriptive rather than a controlled speed claim.

Both cohorts ran GC, repack, compact and GC, followed by independent
whole-object SHA-256 and 64 seeded random 4 KiB range checks per store.
Fresh deep verification found zero missing, corrupt or invalid entries.
All four store directories retained 2,161,774,297 bytes. The continuation
logs record completed baseline checks after the original controller exited.
