# FB-05 · Budgeted online maintenance

**Partial.** This batch implements opt-in byte admission
for online pack rewrites. `-maintenance-max-rewrite-mib` prices each
rewrite by the whole input file, ranks reclaimable density, skips an
oversized item when a later one fits, and budgets reconstruction
families as a unit. Fully dead packs skip rewrite admission and still
go through the existing verified pin, swap, and retire protocol.
The value zero retains the pack-count policy and maintenance remains
off by default.

In the real store fixture, a limit of 49,807 source bytes deferred
both 49,808-byte partial packs and retired a 49,808-byte dead pack.
A 49,808-byte limit admitted exactly one partial pack and produced a
41,520-byte replacement. Exact reads and deep verification passed
after reopen. Focused cancellation and damaged-record controls passed.

The optional `-maintenance-recompress-raw` mode requires the source
byte budget and `-maintenance all`. It screens up to three exact
records per admitted source and 2 MiB of record payload per pass.
Negative results are cached for immutable packs and the selected
block codec. A sample estimates benefit. The ordinary carry path
still verifies every relocated record and its final replacement.

A generated 93,392-byte fully live source was replaced by 1,866
bytes, using a 7,680-byte exact sample. The 99,536-byte entropy
source sampled 24,576 bytes and wrote nothing. The second pass
read no sample payload in either case. Reopened data and deep
verification matched.

`-maintenance-max-stage-mib` bounds concurrent replacement pack
staging, including dictionary records, indexes and footers. An
exhausted pass defers without retiring the source. A 900-byte
test cap allowed an individual pack but forced a group deferral.
The stage directory was empty after return, and offline verification
after reopening found every object intact. A 4 KiB cap admitted
the same group, which published 2,026 bytes across multiple packs.
The option covers pack staging. Grouped authority compaction and
the store's foreground staging have separate controls.

The local multipart expiry rule is enabled with
`-maintenance-abort-multipart-days` and an explicit
`-maintenance-abort-multipart-bucket`. An optional
`-maintenance-abort-multipart-prefix` limits matching keys. Each
pass aborts at most 128 uploads, based on initiation and a UTC
midnight boundary. It uses the existing ordered abort journal
operation and leaves nonmatching sessions and object versions
alone. An injected sync ENOSPC left the recent part visible.
After reopening, retry expired exactly one upload in v1 and J2.
Version expiry and S3 lifecycle configuration APIs remain open.

## Placement and compression replay

The real X9 comparison used 32 source revisions and eight tree
archives, each matched to the F6 SHA-256 manifest. It excludes
the two one-off Go toolchain archives from both engine and model.
With R1 retention, 50% live threshold and a 4 MiB source budget,
the engine's mean pack amplification was 1.031 and rewritten
bytes per net reclaimed byte were 0.433. It rewrote 11.15 MB,
3.6% below the model's 11.56 MB. The engine verified all final
objects by SHA-256. In the follow-up pass, 16 MiB of source budget
removed all 8.89 MB of independently priced compression debt.
At 1 MiB, 84 source admissions were deferred over 32 steps and
the mean amplification rose to 1.277. Model rewrite volume was
within 3.1% of the engine. The mixed 29-object C14 run eliminated
all 16.07 MB of independently priced debt after five 16 MiB passes.

The 1%-hot simulation used 100,000 starting objects and three
seeds. Per-request placement with a 640 MiB source budget and a
40% live threshold gave mean amplification 1.084–1.086 and
rewrite ratios 0.394–0.398. The predictive variant gave
1.071–1.072 and 0.348–0.353, with 10–11% fewer rewritten
bytes. Native class placement still needs a measured ownership
index, cross-class dedup behavior and ingest overhead.

Paced serial staging checks cancellation every 256 KiB of
logical input, with parallel checkpoints at bounded windows.
Eight clean paired trials at 20% duty gave pooled GET p99
4.031 → 5.246 ms and HEAD 3.902 → 5.088 ms. Median pass time
was 2.97 s. The 10% foreground gate remains open. A 25% pilot
had truncated pass windows. Standalone scrub and locator work
still need shared budget accounting. ADR-MNT-03 and ADR-LIF-01
remain owner decisions. [Reproduction](REPRODUCE.md) · [Evidence](results/)
