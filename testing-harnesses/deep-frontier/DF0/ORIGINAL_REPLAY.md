# Independent rerun of the inherited harness

The original test code ran at baseline-equivalent source, in its native
disposable stores, with GF0 and adjacent GF2 registered fixture digests.
The inherited adjacent target had already been observed by GF7 and is
excluded from fresh validation. The signed replay and mixed test were
separate from the later DF0 measurement changes.

| Signed history | Lane | Final allocated bytes | Original / rerun 4 KiB p95 | Original / rerun whole p95 |
| --- | --- | ---: | --- | --- |
| TypeScript v0 to v1 | Archive | 4,829,184 | 32.66 / 31.88 ms | 1,292 / 1,175 ms |
| TypeScript v0 to v1 | GF7 | 4,333,568 | 2.64 / 2.46 ms | 238 / 195 ms |
| TypeScript v1 to v2 | Archive | 5,177,344 | 31.84 / 29.54 ms | 1,269 / 1,207 ms |
| TypeScript v1 to v2 | GF7 | 5,681,152 | 3.65 / 3.99 ms | 241 / 222 ms |
| urllib3 | Archive | 405,504 | 23.03 / 22.46 ms | 103 / 97 ms |
| urllib3 | GF7 | 438,272 | 3.25 / 3.07 ms | 17.4 / 19.1 ms |

Allocations reproduced exactly on all signed lanes. In the mixed
fixture all four lane footprints also reproduced. In one new order,
GF2 4 KiB p99 was 8.98 ms, versus 2.60 to 4.47 ms in the old accounted
pair of orders. GF7 whole p99 was 215.5 ms in that order. With 24
whole samples, the reported p99 is the maximum. Strong archive versus
translation latency differences survive the rerun. The precise sparse
threshold for a compiled lane depends on the run.

Go 1.27 Linux amd64, two CPU quota, overlay store. Both commands
passed without skips. [Raw inherited replay](results/inherited.jsonl)
