# Pythia migration with a disk headroom floor

The recorded Go 1.27.4 build ran authority migration with one worker
and a 128 MiB free-space floor against a fresh copy of the five pinned
Pythia objects. Four ZN7 plans were admitted with depth at most two.
Migration consumed 16.18 CPU seconds and 90,768 KiB peak RSS under
concurrent qualification load. Repack, GC and compaction left
124,395,520 physically allocated bytes, matching the corresponding P8R
screen. Fresh-process S3 readbacks matched all five original SHA-256
digests. Deep verification reported zero missing, corrupt or invalid
records after maintenance.

This successful control complements the injected ENOSPC and insufficient
reserve tests. The free-space floor is an estimate and does not reserve
filesystem blocks from other processes.
