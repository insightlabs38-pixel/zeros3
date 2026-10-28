# Z3-42 filesystem send streams

Verdict: **DEFER** full-send transform; **KILL** incremental-chain transform on
this evidence. Btrfs/modern compressed ZFS remain **DEFER**, without native
production-history coverage. Research only; production, defaults and wire unchanged.

Native producer: Debian zfs-fuse 0.7.0-25+b1, private user/mount namespace,
dedicated 256 MiB file-backed vdev, 64 MiB ARC. No host pools/devices imported.
Full + three incremental sends restored and `diff -r` matched the final tree.
Ten native send outputs cover four snapshots (base, 29-byte edit, 64 KiB append,
rename), independently created datasets, off/lzjb on-disk compression and an
identical repeated send. These are producer workloads, not natural histories.
The legacy send wire is uncompressed; on-disk compression does not establish
compressed modern send coverage. Send-only versus filesystem restore are separate.

Nine hash-pinned Btrfs/OpenZFS regression fixtures add reference-only breadth:
one Btrfs clone stream and eight ZFS base/incremental endian/NATIVE/XDR cases.
Btrfs mounts fail with unknown filesystem; /dev/zfs and loop-control are absent.
User namespaces work; the userspace legacy producer bypasses that kernel gap.
Modern fixtures exceed legacy zstreamdump support, so only CDC byte proofs are
claimed for them. No natural histories, Btrfs restore or modern restore claimed.

Stage A reused Q13's pricing, serialization, closure and cold-range harness by
shell copy/extraction. Native zstreamdump supplies record/payload positions.
Full-snapshot payload reuse justified a 131-line bounded legacy experiment:
`sendz3` accepts little-endian version-1 records, verifies final Fletcher4 and
GUID/length framing, rejects unknown records/features, preserves all accepted
bytes, and groups payloads under existing Concat. Limits: 128 MiB source,
16 MiB payload, 262,144 records, depth four. END reserved tail must be zero.
Unknown/modern streams stay opaque CDC. No ID normalization or new codec/kind.
The candidate decoder needs no native tool, live filesystem or external snapshot.
Format-local from/to GUIDs remain header bytes; byte replay does not claim the
ability to apply an incremental send without its filesystem base snapshot.

All prices include packed reachable objects, roots and 64 bytes/object. The
additive union retains authoritative source CDC; no standalone saving is promoted.

| Native cohort | Source CDC | 4 KiB CDC | Payload candidate | CDC + payload |
|---|---:|---:|---:|---:|
| Four full snapshots | 6,435,214 | 2,603,637 | 2,269,389 | 8,704,603 |
| Full + incremental chain | 2,248,760 | 2,192,840 | 2,250,979 | 4,499,739 |
| Independent/off/lzjb datasets | 4,931,628 | 2,452,748 | 2,132,566 | 7,064,194 |
| Identical repeated send | 2,115,005 | 2,172,887 | 2,115,244 | 4,230,249 |

Native record grouping is worse (8,469,413 B over full history): including each
header destroys payload reuse. Strict payload results exactly equal the native
oracle's objects, cost and sampled reads. Full-send standalone saving is 64.7%,
but fine CDC already captures much of it; additive layout remains larger.
Encoded deltas need little help: last rename adds 755 B under CDC; repeated send
adds zero under every policy. No new ZeroS3 lineage/base transform is justified.
Reference Btrfs CDC is 20,296 B for a 6,299,778-byte clone stream; ZFS reference
base+incremental pairs cost 50,420–53,673 B. These are fixtures, not histories.

22 case inputs / 18 unique SHA256s; 83 serialized full exact proofs, 249 fresh
4 KiB ranges, all reads in declared closures. Maximum full-history amplification:
CDC 53.43x, 4 KiB CDC 8.98x, payload 32.84x; these are logical Source API bytes,
not physical CAS I/O or filesystem-path reads. Full SHA256 proof is separate from
partial-range verification. Native fixture has independently recorded dump spans.

Validation: complete analysis-module tests/vet pass; source identity, overlapping
oracle spans, truncation, GUID/checksum corruption, overflow and trailing records
reject. Fuzz: 198,770 executions / 21.016 s, PASS. A discovered END-tail acceptance
was closed by rejecting nonzero reserved bytes; no test expectation was weakened.
Final harness: 1.393 s, 70,772 KiB RSS (69.1 MiB), excluding compiler; includes
whole-source capture, all exact proofs, accounting and ranges, not a streaming
production decoder claim. No new concurrency surface; no redundant broad race run.

Evidence: analysis `testdata/q14-send` pins, summaries, compressed input/result/
resource records, native restore log and executed producer scripts. Portable
reproducer hash is distinguished from executed source. `sendz3/testdata` contains
an original native tiny fixture; fetch/prepare reuses Q11's bounded pinned fetcher.
Q13's libfuse dependency is reused. Root production/mod/sum diff is empty.

Promotion requires related natural full-send histories, modern compressed/Btrfs
coverage and a layout/lifecycle that beats CDC after retaining its source closure.
Existing native incremental lineage alone does not unlock a new transform.
