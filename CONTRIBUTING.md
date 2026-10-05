# Contributing to ZeroS3

ZeroS3 is a compact, content-aware S3-compatible object store. Contributions
are welcome, especially around interoperability, correctness, operability,
performance, and the content-native storage model.

Before changing core behavior, read:

- [README.md](./README.md)
- [STATUS.md](./STATUS.md)
- [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md)
- [S3_COMPAT.md](./S3_COMPAT.md)

## Development setup

ZeroS3 currently targets Go 1.27.x on Linux.

```sh
go build -o zeros3 zeros3.go
go test ./...
scripts/validate.sh list
```

The production root module has no third-party dependencies. External SDKs and
interop tools live in the separate `testing-harnesses/` module.

## Architectural constraints

A few project constraints are intentional:

- **Standard-library-only production core.** Do not add a third-party runtime
  dependency without a strong architectural reason.
- **One production implementation file.** `zeros3.go` intentionally contains
  the production implementation and has a maintained subsystem map.
- **Logical identity is independent of physical layout.** Manifests describe
  logical chunks, never pack IDs, offsets, codecs, or tier locations.
- **Persistent formats are explicitly versioned.** Incompatible persistent
  state must use a new version/record kind and unsupported readers must fail
  closed.
- **Prefer shared primitives.** Extend CDC, CAS, immutable manifests,
  authoritative roots, reachability, and physical-layout mechanisms rather
  than creating parallel storage models.

## Tests

ZeroS3 has two test layers:

- `zeros3_test.go`: stdlib-only white-box tests for invariants, parsing,
  corruption, crash recovery, concurrency, and persistent formats.
- `testing-harnesses/`: black-box tests using real processes and independent
  clients for S3 interoperability, lifecycle workflows, and benchmarks.

The harness layout is documented in
[testing-harnesses/README.md](./testing-harnesses/README.md). Run focused
validation for the behavior you changed, then broader gates when
shared protocol/storage paths are affected. Documentation-only changes should
use documentation/static validation rather than expensive behavioral suites.

## Public contracts

Documentation is part of the compatibility surface.

Update the relevant document when changing:

| Change | Documentation |
|---|---|
| ordinary S3 behavior | [S3_COMPAT.md](./S3_COMPAT.md) |
| ZeroS3-native protocol | [docs/ZEROS3_PROTOCOL.md](./docs/ZEROS3_PROTOCOL.md) |
| persistent format / compatibility | [STATUS.md](./STATUS.md) and format docs |
| operator procedure | [docs/OPERATIONS.md](./docs/OPERATIONS.md) |
| published performance claim | [docs/BENCHMARKS.md](./docs/BENCHMARKS.md) |

## Storage-safety expectations

Mutation and maintenance changes should preserve the core safety properties:

- incomplete objects do not become visible;
- acknowledged state respects the documented durability boundary;
- corrupt physical content is not trusted without logical verification;
- destructive maintenance preserves content reachable from authoritative roots;
- interrupted operations remain safely recoverable or retryable;
- required replacement copies are durable and verified before old copies are
  removed.

Changes to these boundaries should include tests for the relevant failure cases.

## Performance changes

Performance work should include a same-workload baseline and the costs relevant
to the change, such as throughput, latency, RSS, physical bytes, syscall/file
count, request count, or write amplification.

Existing measurement conventions and results are documented in
[docs/BENCHMARKS.md](./docs/BENCHMARKS.md).

## Pull requests

Keep pull requests focused and explain:

- what changed and why;
- compatibility or durability implications, if any;
- validation that was run;
- relevant limitations.

A small number of coherent commits is preferred.

Do not commit generated logs, benchmark scratch files, credentials, or local
artifacts.

## Style

Prefer deterministic, bounded, explicit code, with comments that explain
non-obvious protocol, durability, security, or concurrency invariants rather
than restating ordinary code.

## Security reports

Do not open a public issue for a sensitive vulnerability.

Use GitHub Private Vulnerability Reporting as described in
[SECURITY.md](./SECURITY.md).