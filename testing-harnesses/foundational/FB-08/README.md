# FB-08 · Native protocol v3

**Partial.** Native batch capability is advertised separately
from legacy CDC and bulk transfer. `replicate -recursive -protocol3`
uses it when both endpoints advertise it and falls back to the
existing path for an older peer. The first checkpoint sends signed
batch describes, descriptor-indexed missing bitmaps and per-item
commits. Requests are bounded by item and encoded-byte counts.

A real signed loopback replication committed 1,000 objects in
nine requests with one shared chunk, and eleven requests with
1,000 distinct chunks. These counts include discovery, enumeration,
describes, negotiation, streams and commit. Data, content type
and user metadata matched across all objects. An equivalent rerun
uploaded zero bytes.
The control rejects unsigned requests, bad versions, excess items,
duplicate destinations and a mismatched whole-object digest.

A separate streaming capability uses a bounded binary descriptor
table and SigV4's chained body signatures. The table fixes record
order, kind, stored length, logical length and full digest. The end
marker binds the table and the verified zero-length SigV4 chunk
closes the request. Commits remain separate signed operations.

The raw relay forwards verified source records over the signed
destination stream as they arrive. An interrupted connection
flushes the authenticated prefix. A matching immediate negotiation
waits for that flush within a bounded interval. The fixture cut
after one of three records and the resumed upload sent only two.
Reorder, drop, duplicate, codec mismatch, extension, bad end
marker and signature tampering failed without a namespace commit.
Process loss can discard verified work that had yet to be flushed.

With `-wire-link-mbit`, the native sender probes small input
samples on slower links and admits independent DEFLATE when
predicted wire savings exceed encoding cost. The optional branch
buffers a bounded batch after selecting its stored lengths.
Higher link settings use the raw relay. Actual signed body
bytes are counted separately from logical bytes.

Three paired loopback tests used eight native Go source ranges
with a 10 Mbit/s destination throttle. Median upload time fell
from 429.2 ms to 149.5 ms. Median signed body bytes fell from
525,669 to 162,959. The entropy control stayed at 525,669 bytes.
On the unthrottled path, raw and higher-rate modes both used
525,669 bytes, with 8.10 ms and 8.14 ms medians.

Batched local-directory sync, source compressed passthrough,
automatic link-rate measurement, durable resumable acknowledgments
across process loss and real WAN qualification remain open.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
