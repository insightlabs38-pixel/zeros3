# Z3-35 — native executable rescue

Status: IN PROGRESS; base `1a0879cfa8cc904398df1aec54e3796b046882a4`.
Production diff empty. No kind/wire number allocated; no promotion claimed.

Checkpoint 1: cmd/z3bin loader, class selectors, unit naming and normalization
wrapper shell-extracted into nativerescue; ELF/PE/gopcln/binref parsers unchanged.
Existing exact normalization streams are repackaged into masked-payload/local-
reference groups; unit IDs use idxref's deterministic two-byte prefix coder.
Grouping uses existing econx unit-aware cuts and idxref component packaging.
Decoder sees only serialized recipe/components, reassembles the original binref
streams and delegates address semantics to the unchanged reconstruction engine.
Region/output/reference/component/metadata limits are checked before allocation.
No executable, source parse state or native address oracle reaches decode.
All bytes, including debug/signing/unknown regions, remain represented.
Exact replay passes ELF+PE across nine independent class selections, six cut
sizes and old-separate versus new-grouped forms. Hostile recipe truncations,
surplus/short components and giant output claims remain canonical tests.
Fixtures are small controlled Q4 compiler outputs; source hashes/flags retained.
Go ELF/PE controlled std/stripped/early families are building with the existing
z3bin overlay producer; no parser/corpus generator rewrite. Economics, tree
nodes, physical repz3 baseline, real releases, reads and final verdict pending.
