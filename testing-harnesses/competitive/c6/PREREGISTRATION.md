# C6 · Physical placement and write cost

B5 already added reconstruction ordering and dictionary-backed restartable
records. B6 prices global closures and B9 groups small authority records.
Use these as incumbents. Audit the final allocation by pack, record, locator,
namespace and slack. The C2 finalization sequence remains the physical baseline.

C5's MobileNetV2 and entropy held-outs retained nearly all original bytes.
Test whether a subsequent full-live repack chooses byte-identical raw packs
and how much it writes to reach that result. Hypothesis: an opt-in verifier
can retain an exact would-be replacement while preserving useful compression
and cross-pack consolidation. Require the same durable pack IDs, byte-exact
reads, deep verify, and a substantial reduction in actual staged writes.
Charge the preflight encoding and verification CPU to the candidate.

Compare small-source, package, Parquet/SST and tensor histories and a bounded
deletion/revision churn trace. Include reconstruction-order and restartable
dictionary lanes, plus an unrelated opaque control. Track first-touch and
warm 4 KiB reads, compaction and repack I/O, and allocation. Treat a shared
compression win as a deployment value only if the added read and rewrite
costs make sense. Preserve existing formats and durability boundaries.
