# FB-J2 · Single-flush transactional journal

**In progress.** Preflight starts at cd9c0d5. The legacy sidecar is only
authoritative for automatic tail discard when both slots form a
coherent pair. An older readable slot can still prove that history at
or below its boundary is missing.

The focused test models partial trailing append bytes and a CRC fault
on disposable copies of real acknowledged stores. Lazy mode uses its
original boot identity and sync mode uses a replacement boot identity.
The owner and inspection path refuse uncertain recovery; the test
checks that journal and watermark bytes survive unchanged. The clean
control reopens and checks the acknowledged object.

The proof boundary covers uncertain damaged tails with a degraded
sidecar. Clean-boundary truncation and coherent rollback remain
separate cases. See [invariants](INVARIANTS.md).

Next experiment compares a compact append-only marker, full
self-contained groups, and those same groups with two alternating
in-file witness slots written before the same flush. Current store
defaults stay under the existing owner gates.
