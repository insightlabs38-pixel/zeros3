# Nested request-local base cache

Two existing depth-two Coreutils stores were independently cloned.
Four fixed 4 KiB reads in each store were compared in fresh command
processes. Both builds used the same retained bytes. The new reader
can keep up to 8 MiB of authenticated base windows inside one request.

| Target frame | Total CAS bytes fetched before | Total CAS bytes fetched with cache |
| --- | ---: | ---: |
| 1 MiB | 914,573,693 | 306,762,122 |
| 256 KiB | 293,979,718 | 166,772,483 |

The new server matched all eight independently hashed signed ranges.
The reported counts are logical CAS bytes. These probes quantify base
rereads on the pinned history. They do not establish concurrent service
latency or memory usage at scale.
