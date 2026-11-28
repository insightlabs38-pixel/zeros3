# Grand Frontier ADR delta

## GF2 · Experimental compiled compressed authority

Record `bitpatch-v1` when publishing a compiled compressed body. Readers
verify source CAS records, check target-frame SHA-256 before ranges are
returned, and check the full object SHA-256 for authority proof and whole
reconstruction. Source chunks remain reachable after anchor-key removal.
A receiver must advertise support for native transfer. Ordinary bundle
export carries the exact CDC-compatible bytes.

The only compiler and publication entry point is the research test
harness. Automatic ingest and archive selection continue with their
current policy. The measured TypeScript pair supports further opt-in
research. Applicability to other compressed histories is unresolved.

## GF7 · Experimental compressed-token authority

Use explicit `deflate-translate-v1` for kind 18. The reader loads
bounded digest-addressed members, verifies each reconstructed frame
before returning a contributing range and checks the full authority
object. Native closure transfer carries its feature requirement. Legacy
export uses exact ordinary bytes. The research compiler publishes through
authority proof and the standard store format fence. Keep this mechanism
opt-in while measuring whole and sparse costs on each history.

## GF8 · Admission and repair qualification

Keep the read paths independently opt-in. Test physical closure costs and
whole and sparse service budgets on held-out histories before selecting
a compiled body. The signed object-scoped recovery path can supply a
missing frame or basis. Treat generic digest repair of compacted
authority objects as an open deployment gate.
