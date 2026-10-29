# Z4 A4 managed streaming — active, unfinished

Status: first checkpoint; no production integration or readiness verdict.
Branch: z4/a4-managed-streaming. Production remains unchanged, managed creation disabled.

The first change replaces research PrefixIndex collision buckets and per-reference scans
with sorted original-index arrays and precomputed unique-prefix widths. Put is constant
work (at most 32 copied bytes); Get narrows sorted intervals without candidate allocation.
ClassIndex retains flat CSR members, class boundaries and sorted handle records;
Put is O(log N), including one class shared by every handle. Class numbering and member
ordinals preserve original occurrence order. Constructor sorting is O(N log N).
Legacy repeated-handle behavior (last class, first occurrence within that class) is retained.

The old implementation was mechanically extracted unchanged into legacy_a4_test.go.
Differential tests compare bytes, errors, trailing bytes and truncated/malformed streams
across 0/1/2/32/257/4096 entries, four prefix widths, deep collisions, ambiguous identities,
duplicate classes and repeated handles. Existing DEX/CLR tests remain intact.
Benchmark (100 ms, one run; indicative, not a production throughput claim):

| Class members | Legacy Put ns/op | Compact Put ns/op |
|---:|---:|---:|
| 1024 | 258.4 | 26.01 |
| 4096 | 1163 | 37.95 |
| 16384 | 3466 | 49.25 |
| 65536 | 9192 | 49.90 |

Each benchmark cycles handles; adaptive iteration counts differ, so the legacy largest
case samples only part of its first cycle. Both paths allocate zero bytes per Put.
This does not satisfy the complete A4 architecture gate. Whole-file input, masked copies,
reference accumulation, component retention and decoder proof remain to be replaced or
bounded; four-size real artifact memory evidence is still required. No completed verdict.

Runtime recovered after the earlier transport interruption. Fetch, toolchain and race
smoke tests succeeded; no OOM kills recorded. 3.8 GiB free on a 32 GiB filesystem at resume.
If disk becomes critical, remove only reproducible scratch first; preserve important
non-reproducible evidence on a separate unmerged backup branch if a stop is unavoidable.
