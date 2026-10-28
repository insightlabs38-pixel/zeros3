# Z3-34 — debug-symbol frontier

Status: COMPLETE research wave; base `f0b34b6`.
Verdict: NO_DEBUG_SYMBOL_TARGET ready for promotion. All three targets DEFER.
Production diff empty; no new kind/wire/default, no large PDB committed.

## Mechanism and exactness
Bounded ReaderAt MSF7 superblock, active free-page map, directory and stream
pages; duplicate/reserved/outside/free-but-referenced pages rejected. DBI/TPI/IPI,
public/global/module streams remain opaque exact blocks, classified by native
llvm-pdbutil. Block/contiguous-stream-run identity is the first measured tier.
Portable PDB metadata root, #Pdb external row counts and separate 0x30..0x37
schemas; Document, MethodDebugInformation, scopes, variables/constants/imports,
state machines and custom-debug rows have exact byte boundaries. Heaps, encoded
sequence points and unsupported records remain opaque. No CLR table conflation.
ELF64-LE sections plus every gap form a whole-artifact exact partition. DWARF
4/5 native-reader snapshots attribute named DIE field churn; compressed DWARF
stays opaque for attribution and exact raw bytes for reconstruction.
Existing Concat serialized recipe/components reconstruct complete size/SHA/bytes
and closed reachability with no parser closure at decode. Unknown/malformed/
bounded-out inputs explicitly select exact CDC in the driver, retaining parser
rejection tests. No semantic field is a correctness dependency.
Caps: file 256 MiB; metadata/directory 32 MiB; MSF streams 65536; PDB streams32,
total row units262144; ELF bounded section table; attribution sections64 MiB,
DIEs1M, depth256. Lab proof buffers complete artifacts, not a production loader.

## Corpus and accounting
Five fixed-directory PRODUCER_WORKLOAD revisions: baseline, two body edits,
early method/function, early type. C rev4 equals rev3 (type edit is C# only),
correctly costs zero. GCC14.2 DWARF4/5; Clang/LLD19.1.7 DWARF5/MSF;
.NET8.0.427 portable, deterministic paths/flags. Tool SDK SHA512 verified;
LLVM official Debian tool packages locally extracted; no service deployment.
`testdata/q4-debug/controlled.json` pins source/output hashes, versions and flags.
REAL held-outs: GNU Hello Debian debug packages 2.10-5 and 2.12.3-1; separate
ELF debug files, not claims about complete release executables. Newtonsoft.Json
13.0.2/13.0.3 official net6.0 symbol-package Portable PDBs (272492/272648 B).
Two toolchain families; public archive/member hashes and URLs in `public.json`.
No natural MSF release pair acquired; controlled MSF evidence cannot replace it.
`q4_fetch_public.py` verifies existing caches and bounded archive/member downloads.
Common lab production pack rule +96 B/reference+32 B recipe; all object bytes,
refs, gaps and debug sections charged. Serialized proof independently crosses
actual Concat wire bytes. Substitutive candidate costs, not additive store wins.

## Results
Candidate/CDC cumulative cost (lower is better), target 4 KiB:
GCC DWARF4 0.869; GCC DWARF5 0.875; Clang DWARF5 1.015;
MSF 0.780; Portable PDB 1.007. Every family has first-ingest premium.
First/body-update cost bytes: GCC4 17700/13648 then6351/13644;
MSF13964/11050 then3391/11048; Portable10160/9039 then7461/8707.
Sensitivity 0/1/4/8/16 KiB: Portable9.918/0.733/1.007/1.024/1.024;
MSF0.780/0.870/0.780/0.701/0.666. Tiny row identities are not free.
Public at4 KiB: Hello110459/106176=1.040; Newtonsoft252990/246559=1.026.
At1 KiB: Hello1.120, Newtonsoft1.191; at16 KiB: Hello1.029, Newtonsoft0.984.
The 1.6% release win is insufficient evidence for semantic promotion.
Native .NET oracle verifies all table counts; 200 stable method/type-signature
keys survive insertion, but all200 tokens and sequence-blob offsets change;
only one encoded-sequence length changes. Body edits change none of those
method descriptors (document checksums/custom data still charged).
DWARF603 stable named DIEs: insertion changes601 DeclLine and200-201 Lowpc;
Highpc1; GCC5 Location25. Counts attribute semantic churn, not byte entropy.
MSF body edit only changes PDB/DBI/module components (packed105+240+230 B);
early insertion also churns IPI/public/global/symbol/directory components.
All raw rows/metrics/churn in `testdata/q4-debug/evidence.json`.
Public pair driver/proof process: peak RSS14116 KiB; small-corpus result only.

## Decisions and portability
Portable PDB DEFER: semantic keyed-reference/sequence-heap rewrite needed to
cash token/heap churn without tiny-row tax; current public cost fails admission.
MSF DEFER: useful controlled page/run reuse, missing natural pair and insertion
churn; deeper symbol/type normalization requires evidence beyond this baseline.
DWARF DEFER: mixed compiler outcomes, source-line plus address churn and public
loss. No generic claim that addresses alone dominate. Preserve compressed/unknown.
Core partitions CORE_PORTABLE; component sink/proof PORT_SMALL_REFACTOR for
production bounded CAS callbacks. Concat contract already exists. Carry exact
partition/grouping and native attribution into Q5; no numbering proposal.

## Validation and reproduction
Canonical exact/hostile/truncation/opaque/CDC controls pass; ELF fixture constructor
shell-extracted from elfz3 tests, adding missing e_version for stdlib validation.
Controlled 25-artifact full replay and native row counts pass. Parser fuzz:
213255 initial,352315 bounded/FPM,121172 final valid-ELF-seed cases,25s each.
Root full/vet pass (cached unchanged production); full lab suite/vet pass.
Final affected tests/native producer replay pass; gofmt and diff checks pass.
Reproduce producer: `python3 scripts/q4_debug_builds.py CACHE GCC CLANG LLD DOTNET`.
Driver: `go run ./cmd/z3debug -target 4096 CACHE/rev{0,1,2,3,4}/portable.pdb`.
Native: `go run ./cmd/z3debugdwarf FILES`; .NET oracle takes DLL+PDB.
