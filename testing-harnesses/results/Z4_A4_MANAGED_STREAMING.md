# Z4 A4 managed streaming — production checkpoint, unfinished

Status: A4 remains active; PR #95 stays DRAFT. No managed merge or final verdict.
Main e4e5b1b retains kinds 1–10. Working branch adds kinds 11/12, cumulative floor 9,
default-off creation, earlier decoders and authoritative CDC unchanged.

## Architecture and exactness

PrefixIndex uses sorted original-index arrays and precomputed unique prefix widths:
Put O(1), Get O(32 log N), no candidate scan/allocation. ClassIndex uses compact
CSR/general lookup or unique-class handle permutations; Put O(log N), constructor
O(N log N). Original class/member order and repeated-handle semantics are preserved.
Mechanically preserved legacy oracles cover deep collisions, ambiguous/repeated IDs,
all prefix widths, duplicate classes, malformed/truncated streams and trailing bytes.
Legacy duplicate Put at 65,536 entries was 9,192 ns versus 49.90 ns compact (indicative).

DEX uses 64 KiB source windows, bounded instruction buffers, staged multi-pass slots,
masked ReaderAt spans and direct component emission. CLR loads bounded heaps, uses
separate metadata/body/layout source windows, compact identities, bounded signature
walks and disk-backed stable reference ordering (position/file offset/sequence).
External sorting uses 4,096-record runs, bounded merge cursors and one record buffer.
No encoder retains original + masked + payload copies or all emitted components.
Decoder getters preflight and consume groups in two bounded passes without retaining
all component inputs. CLR reconstruction borrows heap aliases and allocates exact
IL slot capacity once. Layout/reference semantics and immutable recipe framing remain.

Creation: source <=32 MiB, group <=16 MiB, aggregate groups <=128 MiB, <=65,536 groups.
CLR heaps <=16 MiB each /32 MiB aggregate; <=1M identity rows, <=256K heap entries,
64 MiB estimated context metadata; identity recursion <=256. Method code <=1 MiB,
record <=16 MiB, inspection <=16*source+64 MiB, aggregate token/clause count <=1M.
DEX identity/item counts, prototype hash work, slot/reference counts are bounded.
Production source inspection stays at the existing 1 GiB budget; fallback preserves PUT.

## Production port and discovered defects

AST/shell extraction ports reachable declarations; frozen draft adapter/node framing
is reused mechanically. Five immutable managed roots roundtrip and encoder roots
remain byte-identical. Lifecycle covers full/ranges, off/shadow/prefer, maintenance,
restart, floor 9 and temporary spool cleanup. Malformed candidates fall back safely.

The first real CDC-manifest production probe exhausted 1 GiB inspection on a 718 KiB
CLR input: metadata and method-body windows evicted each other. Separate caches fixed
this to 5,063,041 inspected bytes without raising the budget. A checked I/O regression
also caught swallowed DEX parameter-source errors; diagnostics and repair are retained.
Synthetic multiwindow methods, source errors and shared-body/prototype tests pass.
Future-kind/version test boundaries were updated because kinds 11/12 and floor 9 are
now implemented locally. No cases were removed; original failed suite is preserved.

## Measured evidence and pending gates

Four native CLR raw-source sizes (0.72/5.73/12.78/18.60 MB) previously proved exact
with RSS 22.16/37.72/78.48/104.78 MiB; these are not production-path medians.
Earlier production source 08e72b3d passed 21 exact native fresh-process/store samples
(7 inputs x 3), deterministic roots, independent cold full-SHA proof and floor 9.
Roslyn encoding RSS was 125 MiB and creation 195 MiB: this negative is retained.
Later unique-index/getter source 9c80de34 passed the same 21 exact samples; Roslyn
encode/proof RSS medians 113/164 MiB. Concurrent checks make its wall times unsuitable
for an isolated latency claim. Final exact IL-slot allocation lowers one diagnostic
Roslyn proof to 138 MiB (cold 156 MiB); final isolated medians are still pending.
CDC remains retained; physical representation growth is additive, not storage saving.

Compatibility: native Newtonsoft 13.0.4 ->13.0.3 revisions both represented; portable
full/delta bundles, old clients against compatible server, maintenance and six
snapshot-delete generations pass. Five old binary generations: 79 floor-9 refusals
plus one unsupported old CLI case; before/after file digests preserve nonmutation.
Ten ordinary PUT panic/restart/GC/retry cases and ten real SIGKILL publication
boundaries pass. Crash matrices were run before the final IL allocation refinement.
Focused production exactness tests and relevant index/cache/getter races pass.
Latest library suites pass (CLR 3.906s, DEX 11.321s, indexes 45.152s).
An earlier production full suite passed 194.596s before latest refinements; later
full suite, final isolated native measurements and final race/vet/fuzz are pending.

Durable checkpoints: aa5d7d4, 9551dff, c8e54b2, 450bd56 and this production checkpoint.
Evidence: a4-production-checkpoint-evidence.json.gz and mechanically reusable scripts.
Unmerged branch z4/a4-runtime-backup-20261029 preserves prior interruption evidence.
Runtime recovered; latest local source is now checkpointed separately, not merged.
A0–A3 are complete. Finish A4 gates before any readiness decision; then continue B1–B8.
B2 remains DRAFT_ONLY. No LOC-only criterion or test-integrity exception was used.
