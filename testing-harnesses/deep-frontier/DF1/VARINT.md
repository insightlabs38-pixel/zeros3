# Canonical varint validation

The standard library still decodes the exact byte sequence. A nonpositive
consumption count is refused before canonicality or value-limit checks.
For a successful base-128 decode with value `v`, the shortest encoding
takes `ceil(bit_length(v)/7)` bytes, with one byte for zero.
`bits.Len64(v|1)` preserves the bit length of every nonzero value and
supplies one for zero. The calculated width therefore equals the length
of the standard-library re-encoding on all uint64 values. Canonicality
still precedes the configured limit check. Position and error behavior
are unchanged.

The focused test compares the original criterion against the new reader
over every one- and two-byte input, every seven-bit transition, tenth
byte overflow and overlong cases, truncations, nonzero offsets and
limits on either side of accepted values. It also checks 40,000
deterministic randomized sequences, sticky errors and zero allocations
on accepted input.

## Isolated measurement

The fixed seven-varint microbenchmark fell from a median of 111.7 to
32.3 ns, from five allocation objects and 40 bytes to zero of both.
The native tests used two clients and opposite arm ordering.

| Signed mix | Arm | Process CPU | Throughput | Whole p95 | 4 KiB p95 | GC cycles |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| curl, 240 requests | Original | 12.69–13.72 s | 31.8–36.9/s | 231–280 ms | 6.51–6.84 ms | 198–209 |
| curl, 240 requests | Varint | 9.50–9.72 s | 46.1–48.4/s | 168–188 ms | 5.12–5.19 ms | 150–159 |
| Lua, 1,536 requests | Original | 11.83–12.44 s | 235.1–241.9/s | 27.22–28.24 ms | 5.76–6.27 ms | 918–932 |
| Lua, 1,536 requests | Varint | 9.46–9.50 s | 303.7–305.3/s | 21.05–21.23 ms | 4.75–4.93 ms | 760–766 |

Curl process allocations fell from 144.8–145.2 million to
0.769–0.773 million, and allocated bytes from 4.79–4.83 GB to
3.64–3.67 GB. Lua allocations fell from 106.3 million to roughly
0.997 million, and allocated bytes from 4.32 GB to 3.45 GB. CPU
and allocated bytes moved far less than object counts. Both histories
retained the same physical bytes. The rows retain individual samples,
GC pause counters, exact source digests and stage costs.
[Signed rows](results/varint.jsonl) · [Microbenchmark](results/varint-micro.txt)

In a separate 64-request curl whole-read profile after the varint
change, the output bit writer was the largest flat CPU entry at 19.9%.
The stream-token writer accounted for 29.0% cumulative CPU, and
source-token decoding accounted for 26.8%. Varint validation still
accounted for 8.6% cumulative CPU.
[Post-varint profile](results/varint-whole-profile.txt)
