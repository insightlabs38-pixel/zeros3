# Z3-42 filesystem send streams — Stage A checkpoint

Q14 remains active; no completion verdict yet. Research only; production unchanged.

Native producer: Debian zfs-fuse 0.7.0, private user/mount namespace, dedicated
256 MiB file-backed vdev, 64 MiB ARC. No host pools or devices imported.
Full + three incremental sends restored and `diff -r` matched the final tree.
Ten native send outputs cover four snapshots, independent dataset reexports,
off/lzjb on-disk compression, and an identical repeated send. These are controlled
producer workloads, not natural histories. The legacy send wire is uncompressed;
on-disk compression does not establish compressed modern send coverage.

Nine hash-pinned Btrfs/OpenZFS regression fixtures provide reference-only breadth.
Btrfs kernel mounts fail with unknown filesystem; /dev/zfs and loop-control absent.
User namespaces work. Modern fixtures exceed the legacy native dump tool's support;
no modern restore or semantic parsing claim is made.

22 case inputs / 18 unique sources measured with existing Concat byte proofs,
packed object costs including roots and 64 bytes/object, additive CDC unions,
and three fresh-source 4 KiB ranges each. Native zstreamdump supplies opaque
record and payload positions; it is absent at representation decode time.

Four full snapshots: CDC 6,435,214 B, 4 KiB CDC 2,603,637 B, payload oracle
2,269,389 B; CDC + payload union 8,704,603 B. Record-header grouping is worse.
Full + incremental chain: CDC 2,248,760 B, 4 KiB CDC 2,192,840 B, payload
2,250,979 B. Last rename delta adds only 755 B under CDC.
Independent dataset/off/lzjb: CDC 4,931,628 B, payload oracle 2,132,566 B.
Identical repeat adds zero under every policy.

The full-send payload gap justifies a strict bounded legacy framing experiment.
The already encoded incremental chain provides no corresponding transform win.
No generic BASE or filesystem dependency is introduced into the decoder.
Evidence: `testdata/q14-send` in the analysis module; scripts q14_measure and
oracle/fs-send contain the mechanically reused pricing harness/native producer.
