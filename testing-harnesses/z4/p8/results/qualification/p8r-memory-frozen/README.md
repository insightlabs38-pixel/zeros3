# Frozen P8R memory qualification

PR #120 merged at 3fa2f990f5e3c301613574176a1c3e87c1e92211 with
1c4e4917de2f261e9aff8b87760509716920c4bc as its second parent.
Go 1.27.4 with GOMAXPROCS=4 passed all 36 mandatory steps on the
recorded clean source fingerprint. The qualification JSON preserves
earlier failed attempts. Moving the affected attempts to workspace
scratch resolved the disk-capacity failure and the conventional runs
completed. Normal and race each matched 1,327 test outcomes.

Fresh exact migration and replay passed for three-version fixtures at
256 MiB, 1 GiB and 2 GiB, with migration peaks of 143,264, 189,112 and
201,132 KiB. The concurrent Go 1.27.0 measurements are in the separate
concurrent-g5 directory and use earlier development executables.
