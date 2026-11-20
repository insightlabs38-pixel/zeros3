# Archive discovery across GNU coreutils releases

The C2B commit recorded in source-head.txt ran on Linux amd64 with Go 1.27.4
and two logical Go processors. The fixture used distinct S3 keys for the
pinned GNU coreutils 9.5, 9.6 and 9.7 source tarballs. The 9.6 key became
a depth-1 delta before the 9.7 key was added. Both measured stores started
as independent copies of the same authority state and verified index.
The final migration used archive policy, one base per delta and no
structural or ZN7 alternatives.

| Final migration | Allocated store bytes | Apparent store bytes | CPU | Peak RSS |
| --- | ---: | ---: | ---: | ---: |
| Existing discovery | 125,186,048 | 124,097,386 | 1.04 s | 20,520 KiB |
| Indexed discovery | 68,857,856 | 67,325,596 | 16.03 s | 203,984 KiB |

Allocated size comes from filesystem block accounting after GC, repack,
compact and GC. Both stores include 286,720 allocated bytes for the same
derived index. Both passed fresh deep verification and three independent
S3 readback SHA-256 comparisons. The default planner retained the 9.7
object in CDC. Indexed discovery chose the 9.6 base at depth 2.

The indexed report charges approximately 699 MB fetched per sampled
4 KiB read against 152 kB for CDC. The fixture demonstrates an archive
space tradeoff. One cohort does not establish latency, recall or general
admission economics.

A third copy began at the same state and used cold-tier pricing. It found
the same base and kept the target in CDC. Tier pricing recorded 1.49 MiB/s
candidate replay against its 4 MiB/s floor. That check took 98.66 CPU seconds,
including candidate generation and replay measurement.
