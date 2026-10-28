# Z3-35 — native executable rescue

Status: COMPLETE research; base `1a0879cfa8cc904398df1aec54e3796b046882a4`.
ELF DEFER; PE/COFF DEFER. Production diff empty; no new kind/default/version.

## New mechanism, reuse and physical proof
Loader/class selector/unit/normalization wrapper shell-extracted (131 lines)
from cmd/z3bin; elfz3/pez3/gopcln/binref parsers and address semantics unchanged.
Exact masked payload and local reference records share grouped components.
Native symbol IDs use existing idxref two-byte table-implied prefix coding;
stripped non-Go inputs use binref masked-content identities, separately tested
against explicit symbol/content controls. Ambiguous identities remain raw.
Existing econx function-aware grouping, idxref component packing and index nodes
are reused. Cuts cannot split recorded fields. False-positive scans can cost
space but reconstruction remains byte exact; classes independently selectable.
A bounded NBR1 research recipe names typed regions/groups; experimental NBF1
storage root contains recipe+SHA-addressed index nodes/components. Replay reads
only these stored bytes, verifies every address, delegates reconstruction to
binref, then checks complete original length/SHA/bytes. No source closure,
executable or analysis oracle. These are research envelopes, not repz3 numbers.
Existing raw-section Concat is also serialized/replayed through repz3 as a
baseline; normalized semantics have no installed repz3/production decoder.
All region gaps, debug and opaque PE data are included. No signed PE acquired.
Limits before allocation: object128 MiB; metadata32 MiB; regions65536;
components262144,32 MiB each,256 MiB cumulative; bounded unit/reference streams.
Frozen roots/nodes have separate caps; missing/corrupt dependencies fail closed.
Lab accounting includes 96 B/root ref, actual32 B/leaf ID in index nodes,
recipe bytes,32 B/object envelope allowance, payload/reference/unit-layout bytes
and production pack rule. No ideal masked-payload savings substituted for cost.

## Pinned corpus and comparisons
PRODUCER_WORKLOAD: existing z3bin overlay Go ELF/PE std+strip, eight revisions
r0/r1/r2/r3/early/swap/data/deps; independent unrelated z3tar control retained.
Go toolchain, flags and all36 artifact hashes in q5-native provenance files.
GCC14/Clang19 existing static C three-revision std/strip families reused.
Additional GCC/Clang C++ virtual-call/type families, body+early edits, ELF
stripped/unstripped and Clang PE; source/commands/hashes retained. Q4 debug
findings motivate section/opaque controls; no promoted debug child to compose.
REAL: GNU Hello2.10-5→2.12.3-1 and BusyBox public Debian release binaries;
archive/member SHA pins and URLs in `testdata/q5-native/public.json`.
Use existing q4_fetch_public.py for these verified caches; no repeated downloads.
Canonical19-representation matrix: raw CDC, exact raw-section repz3, legacy
normalized+production CDC, legacy+nodes control, normalized64/16/4/2 KiB,
function4/2 KiB, content identity and eight independent reference-class controls.
Pairwise pdelta costs are labeled sensitivity only; not admission or codec gains.

## Fully charged results
Function4 KiB cumulative candidate/CDC, Go ELF std/strip:0.805/0.688;
Go PE std/strip:0.746/0.901. First-ingest premiums:6.6/11.1/6.9/12.1%.
Legacy normalized CDC:0.825/0.610/0.821/0.670 respectively. Thus new grouping
helps unstripped Go, but does not beat the legacy form on these stripped cases.
Branch+RIP-only selector yields0.799/0.671/0.742/0.890; adding every reference
class can hurt. Explicit content identities can hurt named Go functions too.
Static C grouped4 KiB wins (roughly12-30% in selected fixed histories), but
C++ is mixed: Clang unstripped0.865; GCC1.088; stripped1.262/1.332;
Clang PE1.088. Raw-section Clang C++0.776 is better than normalization.
Public grouped4 KiB: Hello1.152, BusyBox1.111; x86-only1.144/1.110.
BusyBox raw sections0.988, neither grouped case justifies broad admission.
All canonical rows, per-revision costs, stats and pairwise controls preserved in
`testdata/q5-native/evidence.json.gz` (gzip JSON; deterministic artifact).
Admission must handle first-sight premium and relatedness, not select hindsight
per-family winners. Carry these candidate mechanisms and negative evidence to Q6.

## Performance and portability
End-to-end eight-state Go function4 KiB driver under concurrent measurements:
1.12-1.45s versus raw CDC0.49-0.76s; includes analysis, closed proof, packing,
accounting and replay. Separate process2.814s, peak RSS211676 KiB across all
loaded Go sources; user CPU4.844s. Not a decoder-only or production RSS claim.
Current native replay materializes whole output; no point-read gain claimed.
Core normalization CORE_PORTABLE; grouped sink/ReaderAt adaptation and bounded
streaming replay need work before production. No branch/call heuristic is a
correctness dependency. Public-history losses and missing bounded streaming
integration prevent PROMOTE_TO_I2 despite controlled wins. Mach-O not deepened.

## Validation and reproduction
Canonical ELF+PE exact matrix: nine classes,six cut sizes,separate/grouped;
explicit content identity, frozen closure, corrupted/missing objects,truncated
recipes,short/surplus components and giant claims all pass; no cases removed.
Fuzz: initial metadata8239,metadata+component178003,frozen root241379 cases,
25s each. Final full lab suite/vet, affected tests, root suite/vet (unchanged,
cached), whole-tree gofmt and diff checks pass. All19-way corpus runs exact.
Producer: existing cmd/z3bin corpus/ccorpus; q5_cpp_builds.py for C++.
Driver: `go run ./cmd/z3native REVISION_FILES...`; `-only NAME` preselects a
benchmark; q5_summarize.py verifies complete canonical matrices before output.
