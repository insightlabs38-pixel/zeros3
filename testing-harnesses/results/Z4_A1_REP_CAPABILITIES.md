# Z4 A1 representation capability decision

Verdict: **DESIGN_B_PROVEN_FOR_MODEL; production activation belongs to A2**.
A1 is research/test infrastructure only, with no production diff.
Base main 3daab82; original drafts #80/#81 remain open and unmerged.

## Decision and alternatives

Choose cumulative store-format floors, durably raised immediately before root
publication: 7=Model, 8=SST/Parquet, 9=managed. A binary's supported ceiling must
advance only when every earlier tranche decoder is actually integrated.
Never accept a future ceiling merely because its version number is allocated.
A2 must port this tested contract onto its clean Model-only extraction and
repeat the applicable matrix against that actual production candidate.
A3/A4 must retain earlier decoders and repeat their own later-floor gates.

Design A fails accelerator-preservation semantics: A0's actual I1 doctor retires
valid roots of kinds 8–12, although CDC stays exact. This is not a satisfactory
transparent downgrade contract. Design C adds a capability file old I1 ignores;
it still needs a format bump to protect those binaries, creating two mechanisms
without removing the compatibility cost. Design B reuses the durable FORMAT.json
and existing cumulative version/open checks; ZS3R and ordinary identity stay intact.

Tradeoff: an optional accelerator intentionally changes whole-store compatibility.
Old local servers/maintenance cannot open it, including in representation-off mode.
Default creation remains off. Wire clients remain compatible through a server
that understands the store. Portable bundles carry authoritative ordinary content,
not optional graphs or their store floor. Retiring a root does not downgrade FORMAT.
No automatic downgrade/migration is authorized or implemented.

## Tested publication contract

The private Model candidate is immutable #80 source plus only capability/open
and publication changes. Its complete serialized root source is traversed through
bounded production reachability, including all derived children. Every representation
kind contributes its required floor; an unknown kind or unsupported ceiling rejects
before the floor or root is changed. Existing tier initialization/format-6 prerequisites
run before the new floor. The floor is durable before any root-file rename.
The maximum supported floor is seven for the primary candidate. Quarantined managed
code exists in this research binary solely because the source is the unchanged draft;
it is not authorization to include managed code in A2.

Additional isolated #81 and #80 publishers exercise floor 8/9 publication and refusal
by earlier candidates. These are diagnostic publishers, not combined cumulative
production binaries: #81 alone lacks Model and #80 alone lacks SST/Parquet. No claim
is made that either can be deployed as a complete later-generation binary. A3/A4
must validate the actual cumulative integration, not reuse these as promotion proof.

## Real binary matrix and exactness

Generations: pre-I0 cached work/i1a/pre-i0; I0 built from 90123b9;
post-I1C main b5d1762; private Model candidate from #80 43dfe419.
Candidate provenance and source SHA are retained in evidence.json.gz.
The source store contains two exact Model-bearing versions, real base/target
snapshots, and actual compacted packs. Maintenance clones are independent.

| Operation | Older local binary on floor 7 | Candidate / wire behavior |
|---|---|---|
| serve, therefore GET/ranges | refuses open | full and range GET exact |
| stats; verify/basic/deep | refuses | succeeds; exact roots verified |
| GC dry/apply | refuses | succeeds; bases remain reachable |
| compact; repack/apply | refuses | succeeds; exact after restart |
| tier move hot→warm/apply | refuses | succeeds on real packs; exact |
| doctor; explicit retirement | refuses where CLI exists | valid roots retained |
| history prune/apply | refuses | succeeds; snapshots/dependencies retained |
| full/delta bundle export/import | refuses local source/destination | portable full+delta exact |
| sync client | no direct local store API | all generations to candidate succeed |
| replicate client | no direct local store API | all generations from candidate succeed |
| snapshot delete client | server owns local state | all generations succeed through candidate |

There are 48 old-local attempts: 47 actual unsupported-format-7 refusals and one
pre-I0 CLI lacking the retirement flag. The latter is recorded as unsupported CLI,
not a successful floor refusal; its ordinary doctor operation does reach the gate.
No durable payload/namespace/root change in any old-local attempt. Six later-tranche
retirement attempts also refuse floors 8/9. Four change only LOCK owner metadata;
raw changed-path lists include that control-file mutation explicitly.
Every generation imports both actual portable bundles into a fresh store, restores
the target snapshot, and serves exact full/range bytes; destination format stays <6.
Four generations sync and replicate using real authenticated endpoints. Snapshot
delete is a remote client operation, not an invented local prune command: all four
clients delete the base snapshot through the compatible server, then deep proof
passes and the floor stays seven. History pruning is separately exercised locally.
Primary positive matrix has 70 PASS checks; snapshot matrix 12; retained legacy
compatibility phase 54. No failing canonical case was removed or skipped.
Legacy levels 1/2/3/5 open unchanged in off/shadow/prefer, first old-kind root stays
format 6, and ordinary S3 transcripts match the pre-I0 binary.

## Crash and nesting evidence

Five real self-SIGKILL boundaries, every child exit -9:
before capability: floor 1/no optional root; after durable floor: 7/no root;
before root rename: 7/no root; after rename and publication: 7/one exact root.
Restart/deep proof succeeds at every boundary. I1 opens only the before-floor case;
it refuses every floor-7 recovery, including when no optional root was published.
Thus a crash may leave an elevated floor without an accelerator, but cannot expose
a published new-kind root to an older format-6 writer.
A Concat outer header with a Model derived child raises seven and reconstructs
exactly. A Model-ceiling publisher given DEX fails with required floor nine,
leaving format one and no root. Parent-header-only floor selection would be wrong.

The first crash instrumenter matched an ungofmted main signature and installed no
hook: its normal exit triggered an assertion failure. That failed log is retained;
the corrected instrumenter asserts one signature match before building and all
five required cases then execute SIGKILL. No normal exit counts as a crash test.

## Reproduction and next

Private candidates and harnesses stay outside production modules. Builders copy
immutable source and canonical client helpers mechanically; stdlib production is
unchanged. Scripts/probes and compressed raw logs/JSON are in ../z4. Compiler,
producer time and public serving are not conflated with A0 phase timings.
Next A2: clean Model-only extraction, ceiling-seven publication contract, aggregate
work defense, independent real family, exactness/abuse/lifecycle tests and latency.
A1 does not claim any production kind >=8 merged or any later tranche READY.
