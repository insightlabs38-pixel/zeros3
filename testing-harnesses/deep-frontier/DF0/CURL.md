# Fresh curl validation

The four revisions are independently acquired original upstream gzip
archives with XFL 2. Every measured lane uses identical object bytes.
The four-release case also holds an exact four-entry snapshot.

| History | CDC bytes | Archive bytes | GF2 bytes | GF7 bytes | GF7 4 KiB / whole signed p95 |
| --- | ---: | ---: | ---: | ---: | --- |
| 8.10.0 to 8.10.1 | 8,306,688 | 5,267,456 | 6,217,728 | 4,616,192 | 5.01–6.55 / 208–217 ms |
| 8.10.1 to 8.11.0 | 8,343,552 | 5,566,464 | 8,126,464 | 5,758,976 | 7.36–7.57 / 229–232 ms |
| 8.11.0 to 8.11.1 | 8,384,512 | 5,382,144 | 7,524,352 | 4,866,048 | 5.20–5.72 / 219–219 ms |
| Four releases and snapshot | 16,666,624 | 6,467,584 | 21,811,200 | 15,179,776 | 5.77–6.74 / 214–226 ms |

On the first pair, comprehensive post-ingest write calls totaled 7.27 MB
in GF7 and 174.03 MB in the archive. Preparation CPU was 0.86 and
22.95 seconds. The matched whole-read p95 of archive was 2,345–2,381 ms.
GF2 still served whole reads in 26–28 ms, including its smaller native
programs and exact source checks.

The 8.10.1 to 8.11.0 transition had about 291,429 distance corrections,
versus about 45,784 on the first pair. The corrected lane retained more
bytes than archive on this transition. In the longer series each
representation can require its immediate predecessor's original CDC
records, even when the predecessor itself has an authority recipe. The
archive retained less than half the space of GF7 with a far slower
whole-read operating point. Pairwise savings do not compound linearly.

Per lane and order, 64 sparse, 48 medium, 48 whole, 16 HEAD and 16 PUT
requests were independently checked. These p95 figures span two reversed
orders. With 48 whole measurements, p99 is the trial maximum.

The first aggregate trial timed out in the third pair. Both orders
finished for the first two pairs. The third pair and snapshot history
were independently completed in separate, bounded runs on the same
algorithms. The incomplete third-pair samples are retained separately.
[Complete raw rows](results/curl.jsonl) · [Incomplete attempt](results/curl-incomplete-attempt.jsonl)
