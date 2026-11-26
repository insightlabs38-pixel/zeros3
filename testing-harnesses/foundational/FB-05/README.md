# FB-05 · Budgeted online maintenance

**In progress.** The first checkpoint implements opt-in byte admission
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

Source admission is distinct from temporary output, CPU use and
late or revived records. Compression debt, scratch budgets, restart
cuts for each job, X9 comparisons and explicit lifecycle scope remain
open. [Reproduction](REPRODUCE.md) · [Evidence](results/)

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
verification matched. Real-series debt and foreground tests are
still pending.
