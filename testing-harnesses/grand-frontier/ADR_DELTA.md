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
