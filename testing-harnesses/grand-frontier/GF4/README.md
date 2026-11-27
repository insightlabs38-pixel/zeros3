# GF4 · online hot copies and maintenance pressure

The hot-copy controller reduces median sparse-read time on the archived
gzip pair. The strict tail gate and a consistent win against LRU remain
unmet. The controller stays in the disposable test harness.

## Offline and online comparison

The generated trace has 48 epochs and eight eligible regions. Additional
low-rate requests represent the background working set. Scenarios cover
stable concentration, rotations, unexpected hot keys, cold whole-object
restore, pruning and storage pressure, and replication/maintenance pressure.
The simulator uses the signed archive, bit-patch and CDC calibration from
GF2. Static representation allocation is charged at the actual retained
delta. Overlay rent costs 450 equivalent service microseconds per 32 KiB
per epoch. Promotion costs 45 ms, near the observed native median build
time. These prices are explicit experiment assumptions. The objective is
a sensitivity point, not a production service cost.

A 256-state offline DP minimizes the same objective with exact future
demand, capacity, availability and per-epoch writes. A separate exhaustive
three-epoch enumeration verifies the DP. The online controller forecasts
from completed epochs with a variance penalty and retention hysteresis.
A concentration detector was also tested. It changed no action in the
six frozen traces. All methods report misses and retained byte-epochs.

| Generated scenario | LRU misses / writes | Cost-aware misses / writes | Offline misses / writes |
| --- | ---: | ---: | ---: |
| Stable hot set | 187 / 42 | 78 / 1 | 36 / 2 |
| Rotation | 282 / 12 | 252 / 12 | 24 / 12 |
| Surprise | 128 / 2 | 192 / 3 | 0 / 2 |
| Archive and restore | 642 / 36 | 648 / 28 | 480 / 22 |
| Prune and pressure | 209 / 3 | 219 / 3 | 175 / 3 |
| Replication and maintenance | 76 / 2 | 86 / 6 | 53 / 2 |

All overlay methods missed the 50 ms whole-restore target on the 20
archive requests. The static bit patch did well in that trace at 585,728
extra bytes. With the synthetic replication stall it missed the sparse
target 456 times. The response-time and pressure models are separate
from the independent signed native results.

## Native write and read contention

Two foreground workers replay three four-epoch popularity phases and
perform 192 signed GETs, 12 HEADs and 12 PUTs per lane. They share the two
CPU quota with promotions or the native FB-05 maintenance producer.
Only one background rewrite producer can run per epoch. Each promotion
uses the authoritative source, writes the exact original range, then
reads the durable copy before routing new requests. Existing copies
serve independently while new copies are built.

| Signed lane | GET p50 | GET p99 | GET misses above 2.5 ms | Bytes after reclamation |
| --- | ---: | ---: | ---: | ---: |
| Archive and FB-05 | 16.4–16.5 ms | 39–92 ms | 192/192 | 4,853,760 |
| LRU overlay | 0.12–0.17 ms | 49–81 ms | 68–70/192 | 4,919,296 |
| Cost-aware overlay | 0.16 ms | 45–51 ms | 70/192 | 4,919,296 |
| Concentration detector | 0.14–0.17 ms | 46–60 ms | 70/192 | 4,919,296 |

Each overlay materialized 131,072 bytes and retained 65,536 after pruning.
Every maintenance pass carried a genuinely live record from a 402,160-byte
source and wrote 8,368 bytes under the 512 KiB cap. The native pass
overlapped 4–18 signed operations. All reads and acknowledged PUT bytes
were exact, and native deep verification passed before and after reclaim.
The 12 HEAD and PUT samples per lane are diagnostic. The cost-aware
HEAD maxima reached 1.76–5.21 ms versus 0.21 ms for archive. Foreground
quota and outlier limits remain open. Neither live controller qualifies
as the preferred policy on this evidence.

[Evidence](results/) · [Design](PREREGISTRATION.md) · [Reproduction](REPRODUCE.md)
