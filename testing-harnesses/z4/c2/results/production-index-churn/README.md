# Production similarity index churn screen

A Linux amd64 Go 1.27.4 build from source-head.txt indexed a scaled-down
production G3 fixture. The store combined unrelated small objects with
related version histories drawn from 100 seeded bases. The first store
had 12,288 live objects, 48,821 chunk references and 14,398 unique stored
chunks. It held 2.61 GB of logical content with 85.3% CDC sharing.

Build produced 196,608 actual postings in one run, taking 42.29 CPU
seconds and 113,652 KiB peak RSS. The independently counted distribution
contained 165,608 distinct features. Its hottest feature had 24
postings, all returned by the fresh CLI lookup.

The next fixture pass overwrote the existing keys with identical bytes
and added 2,048 distinct small objects. Inspection reported the active
generation stale. Refresh reused the same catalog generation, produced
229,376 postings across two runs, and took 4.31 CPU seconds and 125,848
KiB peak RSS. A fresh CLI query returned the same 24 candidates. The
resulting store had 14,336 current objects and 12,288 historical roots.
Fresh deep verification passed. The query resource files include all
process startup, store replay and index verification.

This run exercises production build, catch-up, deduplication and reopen.
It is a scaled-down churn screen. The 10 million chunk gate, sustained
churn and comparative cold and warm query latency remain open.

The next expansion reached 26,624 live objects, 26,624 historical roots,
29,351 verified chunks and 425,984 postings across three retained runs.
The hottest features held 88 postings each. An independent reader scanned
the encoded posting records and matched all 64 returned IDs from a fresh
query. The index occupied 7,446,528 allocated bytes within the
1,186,250,752-byte store footprint. Refresh used 112.96 CPU seconds and
173,388 KiB peak RSS under concurrent qualification load. Fresh deep
verification passed after the append.

After verifying and opening the expanded index, three warm measurements of
the four busiest features took 110–113 µs per lookup, returning 64
candidates and allocating 1,184 bytes per lookup. This excludes catalog
and run verification and store replay, which are included in the fresh
CLI results above. The page cache was warm and the machine shared CPU
with the running qualification.
