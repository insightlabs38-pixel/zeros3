# Candidate catalog read windows

Catalog lookup on the existing 26,624-root production fixture returned
64 candidates per operation for each of eight hot features. A grouped
reader used 4 KiB windows on neighboring IDs. An independent catalog
fixture read eight exact rows in three calls and rejected a corrupted row.

The first matched three-trial comparison took median 62.9 μs with direct
reads and 137.8 μs with the first grouped reader. Reusing the per-query
scratch brought allocation back to 1,808 bytes and four allocations per
query. In the second matched comparison medians were 115.6 μs for direct
reads and 127.7 μs for grouped reads, with a broad range under co-load.
One representative hot result had only eight adjacent candidate gaps of
eight rows or less across a span of 5,712 catalog rows. The reduction in
read calls did not yield a measured speedup on this fixture. Direct lookup
is the selected production candidate. The isolated implementation diff
and focused correctness logs are preserved with the local experiment.

The report uses Go 1.27.4, GOMAXPROCS=2, 25,000 lookups per trial and
alternating binary order on the same published index. It measures hot
posting and catalog lookup together, excluding store open.
