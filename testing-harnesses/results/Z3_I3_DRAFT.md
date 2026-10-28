# Z3-I3 draft structured integration — work in progress

Status: DRAFT_ONLY, unfinished. No merge authorized by the campaign.
Based on main 4390079 (Q8 merged). Q9 remains blocked draft PR #80.

Checkpoint 1:
- Mechanically extracted SST/Parquet/Snappy/Thrift cores via prodcodecport;
  reuses the existing production Zstd decoder rather than copying it twice.
- Draft kind numbers 9/10; no store, journal, bundle or default-mode change.
- Immediate CAS component writes replace lab Bundle's whole-file retention.
- Strict metadata, page, dictionary expansion, framing and component bounds.
- Legacy SST creation rejected; unknown modern footers stay CDC.
- Nine immutable native-producer lab vectors decode exactly with full reachability.
- All nine production encoder roots match the frozen lab roots byte-for-byte.
- Large-file proof, compatibility, lifecycle and amplification checks remain pending.

The draft is not complete and is not eligible for merge.

Checkpoint 2:
- Native PyArrow 18.1.0 writes a file of exactly 1,073,741,824 bytes:
  267M deterministic INT32 values, Snappy, no dictionary, native metadata padding.
  This is a synthetic memory control, not a natural-history storage gain.
- Full analyzer + CAS + serialized proof + reachability: 98.9 MiB peak RSS,
  83.1 MiB heap growth, 88.8s wall under concurrent validation workloads.
- 4 KiB fresh-source ranges fetch logical component bytes: 1,074,524 at start,
  1,078,145 at middle, 5,651,307 at padded footer (262x/263x/1380x).
  These exclude physical CAS compression and root loading; public ranges use CDC.
- Decoder releases previous-column dictionaries; canonical replay has byte caps.
- Unsupported Pebble7/GZIP fall back; mixed scalar/nested Parquet retains opaque
  nested segments exactly. Native-kind restart tests and all original vectors pass.
- Race and vet pass; structured recipe fuzz: 55,812 executions / 26.1s.
- Full baseline suite passed 226.210s; final-source full suite still running.
- 162-input Q8 production proof matrix and black-box gates still running.
