# C3 · Matched transfer preregistration

C2's ordinary native sync negotiates unique Gear CDC digests against the destination CAS.
An exact repeat can skip all chunk uploads, including across keys in a single trusted store
when chunks are physically available to the CAS. An insertion preserves later boundaries.
A fresh receiver pays for missing chunks; a retry can reuse verified chunks from an
interrupted upload. Ordinary S3 still ships the entire body.

The strongest replication incumbent is C2 bulk CDC plus the optional ZS3C v1 closure
import/export path. Replication relays through an authenticated client. A closure already
advertises destination capabilities and can reuse the current destination key's authority
or components. It is selected only below the missing-CDC byte budget, and a compatible
receiver verifies dependencies, target chunks, whole-object SHA-256 and commit preconditions.
Old peers use CDC. Peer repair fetches specific corrupt or missing digests and validates
them locally. C3 will measure native reuse before changing either protocol.

## Hypothesis and admission

A reversible recipe or explicitly scoped base can move a materially smaller stream for
at least one independent compressed history where CDC has poor overlap. A shifted binary
is a separate lane. Require fewer aggregate HTTP stream bytes than bulk CDC across both
relay legs, after framing. Measure elapsed and CPU separately, and report the implied
bandwidth break-even for repeatable positive deltas. Treat prior optimization as an
up-front analysis cost with an explicit reuse assumption. A full S3 comparison alone
cannot admit a candidate. A persistent format change would require additional qualification.

Run ordinary PUT, local native sync, peer CDC and C2 authority-closure replication on
independent release archives, snapshots, tensor edits, exact and shifted repeats, an
unrelated entropy control, and aggregated source data. Stage destination knowledge
independently for each trial. Start with representative two-revision cases, then expand to
empty, exact, same-key, different-key and partial prior states. Keep no more than one case
and one active receiver on disk. Cap per-command time, use loopback, two Go processors
and two client workers. Alternate repeat order. Verify both source identities and restored
destination SHA-256 and metadata. Use independent TCP taps to count the actual HTTP
stream on each leg, excluding TCP/IP headers, retransmissions, setup and verification.
Compute TCP and application metrics separately. A gzip-on-missing-CDC estimate must be
labeled modeled unless exercised on an actual transport.

Xet operations negotiate exact chunks, pack them into compressed xorbs and describe
reconstructions via shards and xorb chunk ranges. On a local comparator retain C1's
reconstruction defect as a limitation and admit timing only when reconstruction verifies.
Hosted Hugging Face is outside the ordinary C3 comparison. [Xet protocol](https://huggingface.co/docs/xet/en/)

If no independent history wins after CPU and framing, kill the structural candidate and
profile the incumbent instead. Reuse existing exact-closure negotiation and namespace
publication. Any scoped hints must bind an authenticated named object or trusted peer and
fall back safely when stale, absent, unsupported or uneconomical. Avoid store scans.
