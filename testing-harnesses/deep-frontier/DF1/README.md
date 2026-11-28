# DF1 · Faster exact DEFLATE replay

Three compute changes reduce CPU on signed GF7 reads with identical
stored pack allocation and byte-exact output. DF1 keeps the existing
format and feature gate. The engine work is confined to canonical
varint validation, translated output and Huffman input buffering.
[Preregistration](PREREGISTRATION.md) · [Reproduction](REPRODUCE.md)

## Individually measured changes

1. Canonical varint width uses bit length. The proof and differential
   cover valid bytes, overflow, truncation, overlong encodings, sticky
   errors and configured value limits. The accepted microbenchmark
   used zero allocations. The isolated signed change reduced curl CPU
   by 23–31%, although allocation objects fell by over 99%.
   [Varint proof and measurements](VARINT.md)
2. Fused match output appends four verified LSB-first fields to the
   accumulator together. It reduced Lua CPU by about 9–10% and curl
   CPU by 2–6% independently. The generic wider-store rewrite had
   inconsistent native results and was discarded.
   [Writer alternatives and exact controls](WRITER.md)
3. The in-memory decoder uses its existing prefix until it lacks the
   maximum Huffman width. The streaming path keeps eager refill.
   The decode primitive improved by about 12%. The signed change was
   smaller and order-sensitive on curl.
   [Decoder comparison](DECODER.md)

## Combined signed comparison

DF0 incumbent `d0112fb` against DF1 engine revision `e250d76`.
Each range spans two opposite arm orders on Linux amd64, two scheduled
CPU cores and Go 1.27. Curl against curl has identical 4,616,192-byte
native allocation and Lua against Lua has identical 512,000-byte
allocation. Each lane verified exact signed GETs and acknowledged PUTs.
Only the two registered development pairs were loaded.

| Signed mix | CPU baseline → DF1 | Requests/s baseline → DF1 | Whole p95 baseline → DF1 |
| --- | --- | --- | --- |
| curl 1 client, 144 requests | 7.35–7.54 → 4.96–5.07 s | 20.2–20.8 → 30.4–31.0 | 233–245 → 149–161 ms |
| curl 2 clients, 240 requests | 12.37–12.60 → 8.59–8.62 s | 35.5–37.4 → 51.1–54.7 | 244–288 → 163–189 ms |
| curl 8 clients, 240 requests | 12.05–12.45 → 8.12–8.29 s | 37.1–38.3 → 55.7–57.1 | 695–788 → 461–462 ms |
| curl 32 clients, 240 requests | 12.61–12.89 → 8.48–8.66 s | 36.5–37.4 → 55.0–55.6 | 2.88–2.94 → 1.93–1.96 s |
| Lua 2 clients, 1,536 requests | 12.65–13.45 → 8.62–8.87 s | 215–232 → 325–336 | 28.9–29.7 → 18.9–20.5 ms |

At two curl clients, total process allocation fell from 4.79–4.83 GB
to 3.66–3.71 GB and observed GC cycles fell from 203–207 to 158–160.
The 4 KiB p95 fell from 6.15–7.51 ms to 5.03–5.56 ms. At eight
clients the second order moved from 127.33 to 145.07 ms on 4 KiB
requests, while the first improved. Lua whole p99 rose in one order
and fell in the other. Treat operation tails as descriptive for this
host and these schedules. [All combined signed samples](results/combined.jsonl)

## Cold, maintenance and remaining costs

Cold curl first whole GETs measured 131.6–143.3 ms versus
188.9–193.1 ms. Cold 4 KiB measured 9.4–9.8 ms versus 10.6–11.0 ms.
Each arm reports zero resident data-file pages immediately before
reopen. Two cold samples per class characterize first requests.

During the eight-client maintenance arm, both versions wrote the same
8,368 B of live data in the real online carry. The DF1 arm overlapped
27–34 requests and whole p95 was 435–453 ms versus 681–784 ms.
[Cold and maintenance samples](results/extra.jsonl)

In separate signed profiles, the 64-whole-request CPU fell from
12.71 to 8.49 s. The remaining largest single whole-read CPU routine
is byte output from the accumulator at 12.3% flat. Input symbol
decoding consumes 27.7% cumulative. SHA-256 is the largest flat
entry in the sparse profile at 17.0%. The next gains require reducing
replay work or scheduling it better under contention. The improved
32-client point still takes about two seconds at whole-read p95 on
this quota. [Profile receipts](results/combined-profiles.txt)

## Qualification

The focused native regression, race group and eight-client signed
race workload passed. They cover exact token and decoder differentials,
ordinary codec replay, actual old-reader refusal, authority
publication cuts, snapshot retention, pack relocation and scoped
signed recovery. The grouped journal controls passed. Generic repair
still has the previously recorded discovery limitation. Five bounded
fuzz targets exercised the varint differential, bit patch, translation
frames and indexes, and the shared codec parser.
[Verification receipts](results/verification.jsonl)
