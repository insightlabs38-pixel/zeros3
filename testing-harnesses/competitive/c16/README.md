# ZeroS3 C16 · Native transfer from a named ordinary base

**C16_SPECIALIZED.** A signed same-bucket base hint lets a receiver
derive the exact plaintext it needs for a thin closure. On a constrained
link the TypeScript update beat PUT and native CDC. Receiver CPU,
retained base bytes and source planning keep this an opt-in policy.

## TypeScript 5.6.2 → 5.6.3

The old prepared cross-key lane used about 350 KB across both relay
legs and 0.97 s on loopback. An empty or ordinarily seeded receiver
fell back to about 8.4 MB across the relay legs. An explicit ordinary
base enabled a 382,491-byte thin transfer with the lean source policy.
The source archives are 4.17 MB each.

| Model, ordinary base at a different key | PUT | Native CDC | Derived closure |
| --- | ---: | ---: | ---: |
| Loopback | 0.035 s | 0.091 s | 1.511 s |
| 80 MiB/s, 2 ms added per request | 0.079 s | 0.195 s | 1.761 s |
| 8 MiB/s, 35 ms | 0.567 s | 1.365 s | 1.945 s |
| 1 MiB/s, 80 ms | 4.135 s | 8.756 s | 2.786 s |

The constrained same-key case took 2.72 s with a verified thin import.
Per-request delay is injected by the relay. The model includes paced
payloads, without physical WAN loss or routing variability. PUT starts
with target bytes at the client and traverses one relay leg. Native
replication measures both source and receiver legs.

The read-only digest probe verified 22,536,704 plaintext bytes in a
median 86.8 ms with zero allocation growth. The full derived loopback
transfer took 2.38 s receiver CPU and peaked at 87.1 MB RSS. Finalized
receiver allocation was about 9.02 MB versus 8.45 MB for PUT because
the ordinary gzip object and its plaintext dependency both remained.

Source preparation with the original profile used 15.77 s wall and
26.86 s CPU. Restricting locality and candidates and skipping legacy
representation trials kept three successful changes, with 11.48 s wall
and 19.84 s CPU in the shaped run. An unprepared source chose CDC.
At the constrained operating point the lean preparation repays its wall
cost after roughly nine similar transfers, assuming the same savings.
A single upload pays the full preparation cost.

## Other cases

The existing scoped closure reduced SQLite transfer to 16,803 bytes
and restored exact bytes. The gzip probe added 881 bytes without helping
that case. Shifted binary updates favored ordinary native
CDC at about 401 KB versus 4.20 MB PUT. Dtype tensors, independent public
MobileNet weights and entropy received no benefit from gzip derivation.
The local OpenXet dataset used the official client and verified every
revision. Its TypeScript update used 4.66 MB of interface transmit bytes
and 0.115 s upload time. These include the Xet transport and local packet
overhead, with different repository and cache semantics.

## Protocol and verification

`replicate -authority-closure -authority-base s3://BUCKET/KEY
-derive-authority-base` requires an explicitly named same-bucket object.
The receiver advertises optional capability. A signed digest probe
binds a revision and compressed object SHA-256. The import checks the
signed frame and declared root identity before deriving a missing base.
It proves the exact target bytes before publication, verifies gzip
integrity and original bytes, and caps compressed/plain
sizes at 16/32 MiB, and limits preparation concurrency to one. The pin
uses the existing authority record and publication protocol.

Older receivers fell back to CDC, older sources still supplied a thin
closure, and the C15 reader deep-verified the resulting store. Tests
covered duplicate retries, expired credentials, scope and revision
mismatch, invalid frames and byte budgets. Process cuts after component
publication, manifest publication and journal sync reopened with exact
objects and converged on retry. Tests used disposable local stores.
Production power-loss and hosted WAN behavior remain unqualified.

[Reproduction](REPRODUCE.md) · [Raw rows](results/)
