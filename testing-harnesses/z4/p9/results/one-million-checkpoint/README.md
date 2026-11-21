# One million objects

The fixture contains one million signed 4 KiB random-object uploads and probabilistic deletes. The reopened state contained 701,053 visible objects and 298,947 retained historic roots. Journal and checkpoint opens produced the same canonical namespace digest. Two hundred independently sampled object or absence checks agreed with the signed-writer manifest.

Three alternating warm, concurrently loaded trials took 11.71–27.68 s per journal open and 7.29–10.62 s per checkpoint open. The checkpoint open allocated approximately 1.06 GB per operation versus 1.35 GB for journal replay. The derived file occupies 176,676,394 bytes. These measurements do not establish cold-device latency or a ten-million-object limit.

An isolated shadow retained all acknowledged writes in three SIGKILL trials, fell back after checkpoint trailer corruption, rejected earlier journal CRC corruption and truncated a torn tail. A targeted payload corruption on one private raw pack copy returned an explicit S3 error. An independent small DEFLATE fixture behaved the same way after offline repack. In both cases a control object stayed exact and the affected object read exactly again after the original pack bytes were restored. The offline scale fixture journal and checkpoint remained unchanged.
