# DF1 preregistration

Incumbent `d0112fbfc2726f4ca5494b5380a02394d19d39ee`. DF0
attributed 25.1% of sampled whole-read CPU to varint parsing,
98% of estimated Lua allocation objects to its canonical-width
check, and 14.9% of curl whole-read flat CPU to the output bit
writer. Treat CPU, allocation count, allocated bytes and tail
latency as separate outcomes.

1. Replace the canonical varint width calculation without changing
   acceptance, errors, consumed bytes or configured limits. Compare
   the original and variant before touching the bit writer or decoder.
   Measure independent mixed signed lanes, GC, throughput and exactness.
2. Evaluate wider writes or fusion of the exact length/distance code
   fields. Charge bounds, preserve flush and error behavior, and keep
   bytes fixed. Inspect the strongest variant at the output write hotspot.
3. Evaluate the input bit refill/decoder after measuring writer changes.
   Preserve bit positions, truncation and corrupt symbol behavior. Retain
   an incumbent control when an alternative is neutral or worse.

Use frozen Lua and curl development inputs and clean copied native
stores. Exclude the frozen nginx confirmation panel from these runs.
Compare the same query sequence in opposite arm orders and separate
CPU profiles from timed evidence. Record raw requests and process allocation
and GC counters. Recheck native physical bytes and output SHA, old reader
refusal, process interruption, relocation and verified repair. Compute
and wire changes are confined to this batch. The released format,
feature gates, compatibility and production selection continue.
