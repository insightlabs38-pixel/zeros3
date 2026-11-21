# Namespace checkpoint row allocations

The existing 100,000-object signed fixture was opened read-only with the
accepted V2 checkpoint under Go 1.27.4 and two Go processors. The decoder
now stores the key, manifest UUID and ETag in one immutable allocation
per visible row. The checkpoint bytes and journal format were preserved.

Three single-open observations before and after the change put the median
at 601,400 and 401,398 allocations respectively. Total Go allocation
remained about 73.6 MB per open. Wall time varied across the samples.
The focused namespace checkpoint suite passed, including journal and
checkpoint state equivalence, corruption and publication cuts.
