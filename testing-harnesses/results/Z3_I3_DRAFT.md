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
