# Output writer experiments

After the varint fix, the bit writer was the largest flat CPU entry
at 19.9% in the signed curl whole profile.

## Wider stores per output field

This variant emitted a single buffered byte directly and copied wider
batches from a little-endian word. The exact differential compared
bit offsets 0 through 7, zero through 32-bit fields, flush boundaries,
and an injected write error. The native whole and ranged receipts
verified identical output.

The writer microbenchmark shifted from a median 53.9 ns to 56.1 ns
for the fixed field group. Signed CPU moved in different directions
between the two orders. On curl it was 10.20 and 10.18 s in the parent,
and 10.27 and 9.74 s for the candidate. On Lua it shifted from
9.93 and 10.22 s to 10.19 and 9.95 s. This is insufficient evidence
for adopting the generic rewrite. Preserve the variant and raw results.
Replay the saved patch against the varint-only source using
`git apply --unidiff-zero` in a disposable checkout.
[Variant patch](results/word-writer.patch) · [Signed rows](results/word-writer.jsonl) ·
[Microbenchmark](results/word-writer-micro.txt)

## Fused match output

A match writes the literal/length code, length extra bits, distance
code and distance extra bits consecutively in the same frame. Because
the original codewords are LSB-first and the four widths total at
most 48, a single accumulator append can form their concatenation
without crossing the 64-bit accumulator bound. The experiment checks
complete frame bytes against the original per-field stream writer for
24,000 tokens and all eight initial bit offsets. Native checks compare
the same finalized authorities and original compressed bytes.

| Mix | Varint CPU | Fused CPU | Varint requests/s | Fused requests/s |
| --- | ---: | ---: | ---: | ---: |
| curl | 9.62–9.72 s | 9.17–9.38 s | 45.6–49.2 | 48.0–50.3 |
| Lua | 9.58–9.95 s | 8.65–9.00 s | 292–301 | 321–332 |

Both orders reduced total CPU. Lua whole p95 fell from 21.70–21.82 ms
to 19.38–20.20 ms. Curl whole p95 moved in both directions and the
two sparse p95 probes rose from 5.24 and 4.85 ms to 5.36 and 5.73 ms.
Those tails remain a test for the combined reader.
[Independent fused rows](results/fused.jsonl)
