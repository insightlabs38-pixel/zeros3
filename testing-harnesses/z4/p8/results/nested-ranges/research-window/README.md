# Mixed-frame Coreutils history on the research branch

Both lanes used the same independently cloned [Coreutils seed](../../../../c2/results/research-coreutils/README.md)
and the recorded clean Go 1.27.4 binary at two Go processors. The
unmodified depth-one base had 1 MiB ZW1 frames. Both lanes used the
persistent index, admitted the 9.7 target at depth two and enabled the
archive policy. Only the tuned lane passed `-window-kib 256`.

| New target frame | Allocated bytes | Apparent bytes | Mean CAS bytes fetched per sampled 4 KiB read | Migration wall time |
| --- | ---: | ---: | ---: | ---: |
| 1 MiB | 68,857,856 | 67,325,596 | 698,777,420 | 10.282 s |
| 256 KiB | 68,943,872 | 67,427,694 | 197,794,795 | 8.811 s |

Both lanes passed deep verification and three independently hashed
signed GETs after GC, repack, compact and GC. The tuned target fetched
71.7% fewer CAS bytes per sampled read and used 86,016 more allocated bytes
(0.125%). The trial used the two archive-policy probes from the migration
report. The read cost remains high against the same 151,872-byte CDC
estimate. One matched cohort supports an opt-in control.

A separate binary built from stable `main` also passed deep verification
of the maintained 256 KiB store. Its source and binary digests are recorded.
