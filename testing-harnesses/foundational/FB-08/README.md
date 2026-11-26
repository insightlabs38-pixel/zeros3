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

Descriptor-bound streaming, prefix resume and link-aware wire
selection remain in progress.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
