# Z3-34 — debug-symbol frontier

Status: IN PROGRESS; base `f0b34b6ed9a34dbcef56fd745e0a55f6e65f9e43`.
Production diff empty; no kind/wire/default decision.

Checkpoint 1: bounded ReaderAt MSF7 page/stream directory, Portable PDB metadata
root plus separate 0x30..0x37 schemas and #Pdb external-row widths, whole-ELF
exact section partition. All unknown records, gaps and unused pages stay exact.
Serialized existing Concat proof reconstructs complete bytes/size/SHA and closed
dependencies; parser closures never reach decode. File <=256 MiB, directories
<=32 MiB, bounded stream/row/page counts; duplicate/reserved/outside pages fail.
Existing lab accounting charges conservative 96-byte references and production
packing. First-sight and subsequent revisions are measured separately.
Five fixed-directory controlled revisions include body, early method/type edits.
GCC14 DWARF4/5, Clang19 DWARF5/MSF, .NET8.0.427 Portable PDB; source/flags and
tool versions retained. llvm-pdbutil/.NET MetadataReader independent oracles.
Initial grouped 4 KiB candidate/CDC totals: GCC4 0.869, GCC5 0.875,
Clang5 1.015, MSF 0.780, Portable PDB 1.007. Controlled, not natural histories.
Every candidate has a first-ingest premium; tiny rows pay substantial reference
tax. No promotion before held-out histories and semantic churn attribution.
Exactness/hostile/truncation tests and race pass; parser fuzz 213255/25s.
Real GNU Hello and Newtonsoft release artifacts fetched/hash-pinned for held-out
evaluation. Native attribution, breadth checks and target verdicts pending.
