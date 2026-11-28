# Buffered Huffman input

The in-memory reader already holds enough bits for either decode
path when its accumulator contains the maximum DEFLATE code width of
15 bits. Delay refill in that case. The short-code table sees the
same nine-bit prefix. The slow path has every possible code bit.
Extra-bit reads still refill on demand. End-of-input and streaming
refill follow the original path.

The differential compares eager and deferred decoding with fixed
literal and distance tables, a complete table with depth 15, an
incomplete singleton and an empty table. It checks symbol, exact error
and logical bit position after interleaved bit consumption in memory
and streaming mode. It also verifies native translated output through
the existing byte-exact reconstruction control.

The 4 KiB Huffman primitive moved from median 21.92 to 19.31 µs
with zero allocations in both arms. On the fused writer, Lua signed
CPU moved from 9.04–9.19 s to 8.68–8.77 s and whole p95 from
20.22–20.95 ms to 19.47–19.53 ms. Curl CPU moved from 10.21 to
9.56 s in one order and 9.70 to 9.71 s in the other.
Treat curl's isolated decoder result as small and order-sensitive.
[Signed rows](results/decoder.jsonl) · [Microbenchmark](results/decoder-micro.txt)
