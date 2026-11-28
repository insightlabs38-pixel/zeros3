# Grand Frontier verdict

The tested frontiers support two opt-in compressed-history readers.
Keep CDC for latency-sensitive traffic, use each compiled form only
where measured space and request costs justify it, and retain the
archive for compact or restore-light histories. Current defaults and
the J2/v1 durability contract continue.

| Pillar | Observed contribution | Decision and limit |
| --- | --- | --- |
| [GF0](GF0/README.md) | 36 pinned native baseline rows over nine families | CDC and current archive both remain controls. |
| [GF1](GF1/README.md) | Exact shared-closure oracle, bounded searches and native capped selection. Modeled TypeScript savings within one 4 KiB block. | Partial. Work caps can reject an expensive point. urllib3 shows that decoded bytes alone cannot guarantee latency. Joint arrangement and calibrated online selection remain open. |
| [GF2](GF2/README.md) | Directly executable shifted bit copies give signed TypeScript v0 to v1 whole p95 about 22–27 ms in 5.415 MB. | Specialized opt-in. Adjacent held-out pair loses most of that space advantage. Fast witnesses lost on bytes and hot overlays need stable coverage. |
| [GF3](GF3/README.md) | Shared pages saved 3.9% on the controlled cohort and were reclaimed after the last referent. | Research only. Existing XD and archive beat fixed banks on real rows. Relaxed discovery helped recall. |
| [GF4](GF4/README.md) | Exact hindsight and online comparisons with physical concurrent maintenance. The bounded carry wrote 8,368 B from 402,160 source B. | Research only. Native rent-aware policy missed the 2.5 ms target and did not consistently beat LRU. |
| [GF5](GF5/README.md) | Residual windows and retained FPE anchors on three true training checkpoints. | Research only. Native ZN7 used 63.79 MB and 331 ms whole p95 versus 69.06 MB and 833 ms. |
| [GF6](GF6/README.md) | Verified receiver-aware thin closure saved both wire bytes and wall time on a prepared receiver with low RTT. | Specialized research. Raw relay leads above the measured staged-request crossover. Physical WAN and real fleet workloads remain unqualified. |
| [GF7](GF7/README.md) | Exact source-token translation and distance correction recover compressed-domain sharing across Huffman changes. | Specialized opt-in. At 5.681 MB the adjacent held-out point uses more space than archive and serves much faster. The development history wins both against archive. |
| [GF8](GF8/README.md) | Named native lanes, signed mixed replay, independently rebuilt held-out stores, exact snapshot and repair evidence, bounded race and fuzz. | Integrated research qualification. A production policy remains gated by measured tails and operational coverage. |

## Original contribution and practical frontier

The contribution combines a physical dependency-union objective with
compressed-output range programs. In the GF7 reader, addresses and
code tables describe selected original compressed source runs. The
decoder produces the target's original codes after field-level
correction and verifies each local reconstruction. Prior exact
re-encoders and native canonical storage share some primitives.
The artifacts support a research report, with no priority claim.
[Technical report](RESEARCH_REPORT.md) · [Related work](PRIOR_ART.md)

For held-out TypeScript, translation occupied 5,681,152 B with signed
4 KiB and whole p95 of 3.65 and 241 ms. Archive used 5,177,344 B with
31.84 and 1,269 ms. The comparable compiled build drops from
8,065,024 B with GF2 to 5,656,576 B with GF7. On held-out urllib3
translation offers an additional read-heavy point. On a small changed
compressor case all three native-build lanes reached the same physical
floor. Level-19 zstd patch-from used 8.270 MB for the adjacent pair
with standalone whole reconstruction and no sparse serving guarantee.
These results establish observed points among measured lanes
with pinned dependency and read contracts. A broader population
and other pack layouts could change the frontier. [GF7 tables](GF7/results)

In the write-accounted mixed replay, CDC, bit patch and translation
occupied 8.39, 5.41 and 4.33 MB. Whole p99 rose from 11–14 to 39–118
to 228–341 ms. Archive occupied 4.83 MB with 2,277–2,324 ms whole p99
and 162.48 MB of process writes after ordinary ingest. The trade-off
selects different lanes under different foreground budgets.
[GF8 tails and write accounting](GF8/README.md)

## Production gates and remaining research

- Bound admission using observed mixed-read, repair and maintenance
  costs. The inherited optimization policies remain the production
  comparators. Find a way to bound random replay work on a larger
  target and validate cross-format and longer retained histories.
- Keep experimental format and protocol selection opt-in. New readers
  enforce the feature and verify local reconstruction. Old readers
  refuse unsupported live formats. Exact native transfer and ordinary
  snapshot bundle import have passed. Preserve those checks in any
  future writer or migration.
- A snapshot-pinned basis survives expiry before dependent publication.
  Object-scoped signed closure recovery repairs missing frames and
  bases across reopen. Generic digest repair requires a discovery fix
  for objects whose representation already retired the ordinary members.
- Signed foreground tails use two workers and local cached I/O. Demand
  realistic hot-set shifts, per-device and cold-cache measurements,
  controlled maintenance and extended namespace load before broad
  admission.
- Real WAN request scheduling, production allocator and descriptor
  pressure, and physical power loss require separate qualification.
  A local SIGKILL validates the exercised publication fences.
- Reproduce with the pinned harness and inspect failed arms alongside
  selected points. [Bounded replay](GF8/REPRODUCE.md) · [Ledger](LEDGER.md)
