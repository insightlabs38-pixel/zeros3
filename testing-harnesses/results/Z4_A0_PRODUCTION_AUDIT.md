# Z4 A0 production audit — checkpoint

A0 is active; no production change or promotion decision yet.
Audited main b5d1762; immutable drafts #80 43dfe419 and #81 0a37846c.
The Z4 rule replaces the old LOC veto; implementation quality decides promotion.

Declaration-level AST audit finds 260 new qualified functions/methods in #80,
255 in #81, and no identical added qualified helper shared between the two.
This does not prove absence of equivalent duplicated algorithms; manual review
checks builder boundaries and format-specific contracts. Existing Zstd/core CAS
is shared rather than copied a second time inside either draft.

Model uses ReaderAt metadata parsing, 256 KiB plane/XOR windows and bounded bases.
SST/Parquet emit directly into CAS with bounded page/block scratch; Parquet's
preflight and encoding passes need isolated latency measurement. Managed code
retains full masked copies, payload/group state and large identity/reference
structures; its disabled ordinary creation remains a real efficiency blocker.

Compatibility concern from source: I1's doctor retirement acts on every RepFault.
An unknown valid new-kind root may therefore be retired as faulty, despite exact
CDC reads and fail-closed GC. Disposable-store operational evidence is pending;
old read success alone cannot establish full downgrade semantics.

Twelve inputs were pinned before latency results: three Pythia checkpoints,
GGUF fidelity control, largest cached RocksDB file per none/Snappy/Zstd codec,
two DuckDB files, the existing native 1 GiB Parquet, one public DEX and CLR each.
No producer reruns/downloads are needed for this audit. Phase instrumentation will
separate ordinary CDC setup, encode/CAS, flush, full proof, decode and publication.
