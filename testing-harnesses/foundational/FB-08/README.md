# FB-08 · Native protocol v3

**In progress.** Native batch capability is advertised separately
from legacy CDC and bulk transfer. `replicate -recursive -protocol3`
uses it when both endpoints advertise it and falls back to the
existing path for an older peer. The first checkpoint sends signed
batch describes, descriptor-indexed missing bitmaps and per-item
commits. Requests are bounded by item and encoded-byte counts.

A real signed loopback replication committed 1,000 objects in
nine requests, including both discovery requests, enumeration
and one shared data transfer. Every object's data, content type
and user metadata matched. An equivalent rerun uploaded zero bytes.
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
Link-aware wire selection remains in progress.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
