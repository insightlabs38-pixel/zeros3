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

## Prototype checkpoint

The compact CRC marker (A), begin/records/SHA-256 footer (B), and B
plus a same-file witness (C) all use one sync per append. C writes the
alternate witness before that sync. The older Design B controls write
their seal afterward. The same 300-byte payloads and acknowledgment
edge apply to all measured variants.

Median operations/s across three rotations of 256 local overlay
groups per width. Results measure the journal path in isolation.

| Frames/group | v1 lazy | v1 sync | J2-A | J2-B | J2-C |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 4,538 | 3,215 | 4,571 | 5,156 | 5,120 |
| 4 | 17,296 | 10,732 | 15,917 | 16,906 | 12,606 |
| 8 | 27,437 | 19,013 | 25,704 | 24,180 | 22,680 |
| 32 | 84,838 | 62,142 | 80,358 | 90,827 | 79,349 |

A and B recovered all 28 incomplete or out-of-order pre-sync bodies.
Each refused identifiable damage followed by a valid later group.
Damage to the final acknowledged footer and exact-boundary loss
remain undetectable under those candidates' crash-only policy.
C refused both with the witness intact, and refused a combined
footer and newest-slot fault when older evidence survived. An
ahead-of-body witness also refused a valid power-loss model case.
Coherent log and witness rollback remained undetectable.

The initial B/C scanner created a 1 MiB reader per group. Validating
the already bounded frame slice removed that cost. Tmpfs measurements
are separately labeled and the overlay table uses the optimized
reader. That scan is in memory. Engine throughput, signed reads,
cold open, real process fault gates and format selection remain open.
