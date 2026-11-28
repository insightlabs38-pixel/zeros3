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
