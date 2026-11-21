# Coreutils archive selection on the research branch

The binary used Go 1.27.4 with two Go processors at the recorded clean
source commit. Each mode used an independent clone of the earlier
[Coreutils seed](../coreutils-cross-key/README.md), with the same verified
index, the same 9.5 and 9.6 authority state, and 9.7 as a separate key.
Migration used archive policy, one delta base, and disabled structural
and ZN7 alternatives. The indexed mode added `-indexed-discovery`.

| Discovery | Allocated bytes | Apparent bytes | Migration wall time |
| --- | ---: | ---: | ---: |
| Existing | 125,181,952 | 124,097,386 | 0.870 s |
| Persistent index | 68,857,856 | 67,325,596 | 9.713 s |

Both modes ran GC, repack, compact and GC before deep verification and
three independent signed GET readbacks. All six readback digests matched
the pinned release archives. Allocated bytes include the derived index.
The indexed mode admitted one depth-two delta and reduced allocated size
45.0%. It fetched 698,777,420 bytes per sampled 4 KiB read on average,
versus 151,872 for CDC. This tradeoff applies to the stated archive
policy and seed. The earlier cold-tier control refused the same
relationship on throughput grounds.
