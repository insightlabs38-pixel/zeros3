# FB-J2 · Single-flush transactional journal

**PARTIAL · opt-in research.** Preflight starts at cd9c0d5. The legacy sidecar is only
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
reader. That scan is in memory.

## Integrated research path

B is implemented in the engine for new opt-in stores. It retains the
existing namespace and CAS publication path, uses one journal sync per
group, and supports checkpoint adoption and deep verification. Its
proof boundary and rollback limits are in [FORMAT.md](FORMAT.md).
C remains a prototype because an ahead witness costs host-crash
availability. A offers fewer bytes per group and weaker evidence.

The optimized signed overlay test compared equal 1 KiB and 32 KiB
work at 1, 8 and 32 writers, plus a PUT/HEAD/DELETE mix. Relative to
v1 lazy, median throughput ranged from −22% to +35% across the
nine points. Three points missed the −5% threshold. The 32 KiB,
8-writer PUT cell moved from −15% initially to +6% after bounded
buffer reuse. A five-round focused check at that point measured
1,643 → 1,886 ops/s and 4 KiB GET p99 2.28 → 2.01 ms.
The mixed 32-writer GET p99 rose 9.56 → 11.98 ms in the wider run.
A separate profiling pass of the initial build changed several
relative results. The data do not establish a general throughput
or foreground tail pass. In the focused CPU profile appendFrames
accounted for 2.7% inclusive, primarily file write and sync.
Cold device qualification remains open. Reopen times use ambient
page cache.

The process-kill run covered 13 SIGKILLs with eight concurrent
writers and 1,608 externally recorded acknowledgements. All
survived, and three torn tails recovered. Also exercised grouped
checkpoint adoption, checksum failures, fail-stop on EIO, exact
replay, golden format, and old-reader refusal. The recovery proof
assumes conventional sync semantics. Correlated media loss and
hardware interruption remain open.

Continue FB-04 with both journal backends. Revisit the foreground
tail, larger publication groups and independent witness options
under the FB-10 integration gate.
