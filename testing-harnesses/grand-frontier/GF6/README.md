# GF6 · choose the closure for the receiving peer

A signed v3 descriptor batch proves which explicitly named basis is a
viable planning candidate. Exact SHA-256, size, object scope and prepared
authority state are required. The source enumerates single and pair
programs, proves the selected record, and transfers it through the normal
signed native closure import. Receiver proof and namespace admission
remain existing protocol operations. The coordinator is a research test.

The generated input has two 256 KiB bases. With the near prepared basis,
seven edits took a 180-byte XD program. The signed native transfer
accepted 1,155 bytes for the prepared destination. The two-basis
control used both independently verified bases named by its current
destination record and accepted 1,244 authority bytes. Neither fixture
makes a cross-namespace candidate query.

| 7 edits, 1 MiB/s per leg | Plain two-leg relay | Current v3 | Receiver-aware |
| --- | ---: | ---: | ---: |
| 0 ms additive delay | 0.504 s | 0.197 s | 0.038 s |
| 20 ms | 0.546 s | 0.571 s | 0.239 s |
| 80 ms | 0.667 s | 1.504 s | 0.766 s |
| 150 ms | 0.815 s | 2.618 s | 1.419 s |

The relay, v3 and receiver-aware paths transferred about 524,288,
361,284 and 7,520 bytes respectively on both legs and both directions.
These totals include control bodies. HTTP headers are excluded.
The three paths issued 2, 23 and 9 requests. The raw relay wins at high
delay. The simple estimated break-even against plain is
`((524288-7520)/rate - source_prep_seconds)/7`. With ~16 ms of
prep it is about 68 ms at 1 MiB/s, 3 ms at 8 MiB/s, and about
280 ms at 256 KiB/s. These last two are projected rates. Real
parallel requests and source contention can move the crossover.
For many consumers the available-basis hit rate changes how often
negotiation pays. Under the measured fixture, a miss switches to CDC
after one extra scope-bounded probe. Full preparation cost is separate.

At 0 edits v3's existing reconciliation was cheaper than probing.
At 1 and 1000 edits the prepared peer accepted 1,072 and 6,870 closure
bytes. The source-preferred older base was unsuitable and fell back
to exact CDC on the near receiver. On empty, ordinary near and
same-name unrelated peers the new policy chose no unverified base.
A truncated successful export left no published target. After destination
reopen the retry published exact bytes. Process-cut storage recovery is
covered by GF2 and the earlier native transactional control. The shaped
lab charges application bytes and optional delay, with no physical WAN
or production multi-tenant inference.

[Rows](results/signed.jsonl) · [Reproduction](REPRODUCE.md)
