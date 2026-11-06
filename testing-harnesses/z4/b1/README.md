# B1 reproduction
Research-only; no production storage format or authority change.

1. `python testing-harnesses/z4/b1/prepare.py` inventories cached pinned artifacts,
   verifies availability and generates explicitly labeled deterministic controls.
2. `go -C testing-harnesses/z3 build -o /tmp/b1 ./cmd/z4similarity`.
3. Use `measure.py /tmp/b1 -manifest MANIFEST -out STORES` for one isolated run;
   stdout is JSONL, stderr includes process RSS, CPU and exit status.
4. `/tmp/b1 -replay STORES/FAMILY-cdc` reopens only serialized roots and deltas,
   authenticates dependencies and complete original revision hashes in a fresh process.
5. `/tmp/b1 -scale 100000` (then 1000000, 10000000) measures four-feature sorted
   tables with actual temporary writes, sync, binary-search queries and cleanup.

The all-algorithm study time includes discovery, candidate trials, CDC/fine-CDC
baselines and serialization. It is not the throughput of an optimized ingestion
policy. Algorithm totals are conditional on the common selected historical
store and best candidate; they are not independent retained-history policies.
All four query indexes are charged at 12 bytes/posting plus 32 bytes/object.
CDC/fine CDC use the existing lab production compressor and 96-byte pack record
plus locator overhead; every root reference is charged 36 bytes. Common pack
header/footer costs are excluded. Experimental envelopes add their own headers.
Pdelta is an existing estimated lower-bound instrument, not a serialized codec.
