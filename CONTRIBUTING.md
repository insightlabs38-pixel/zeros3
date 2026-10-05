# Contributing to ZeroS3

ZeroS3 is intentionally capability-dense: a small set of storage primitives
supports a large feature surface. Contributions should preserve that property.

Before changing core behavior, read:

- [README.md](./README.md)
- [STATUS.md](./STATUS.md)
- [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md)
- [S3_COMPAT.md](./S3_COMPAT.md)
- [testing-harnesses/README.md](./testing-harnesses/README.md)

## Core architectural constraints

### Keep the production core dependency-free

The root module is Go standard library only.

Do not add a third-party runtime dependency to the root module without an
extraordinary architectural reason.

External SDKs/tools belong in the separate `testing-harnesses/` module.

### Keep one production implementation file

`zeros3.go` is intentionally the production implementation.

The single-file core is no longer just historical contest residue; the source
map and subsystem boundaries make it a deliberate compactness constraint.

Do not split the implementation into packages/files merely for conventional
appearance.

### Preserve logical / physical separation

Logical object identity is:

```text
CDC chunks -> SHA-256 identities -> immutable manifest -> roots
```

Physical storage is:

```text
loose or packed -> compression/locality -> hot/warm/cold placement
```

Do not put pack IDs, offsets, codecs, or tier locations into object manifests.

### Version persistent formats explicitly

If a change creates state an older reader cannot safely interpret:

- introduce an explicit version/record kind;
- raise the relevant format before publishing incompatible state;
- make unsupported readers fail closed;
- document the compatibility impact in `STATUS.md` and format docs.

Never silently repurpose an existing persistent field/record number.

### Reuse authoritative mechanisms

Prefer extending an existing primitive over creating a parallel subsystem.

Examples:

- reachability should derive from authoritative roots;
- repair should reuse logical chunk identity;
- snapshot restore should reuse transfer/commit primitives;
- physical maintenance should not create a second logical object model.

A new primitive is strongest when it unlocks several capabilities.

## Navigating the large source files

Both `zeros3.go` and `zeros3_test.go` begin with subsystem maps.

Use targeted navigation:

```sh
rg -n "symbol|relatedSymbol" zeros3.go
sed -n 'START,ENDp' zeros3.go
```

Prefer narrow ranges and focused diffs.

Avoid repeatedly dumping the complete source/test files during review or
agent-assisted work.

If line movement changes the source/test maps, update and validate them.

## Validation philosophy

Run the least expensive test that can actually detect the class of defect just
introduced.

Examples:

- parser change -> parser-focused tests;
- S3 handler change -> focused handler tests, then external S3/client gates;
- pack encoding change -> focused pack/direct-pack tests;
- reachability change -> focused GC/tier/snapshot tests;
- docs-only change -> links/claims/static checks.

Do not use the full root/race/scale suite as an edit loop.

After executable behavior stabilizes, run the broader gates justified by the
changed surface.

List repository validation stages with:

```sh
scripts/validate.sh list
```

External harness documentation is in
[testing-harnesses/README.md](./testing-harnesses/README.md).

## Docs-only changes

Documentation is treated as part of the public contract.

For docs-only changes:

- verify local Markdown links;
- check command names/flags against source;
- check protocol constants/version numbers against source;
- check S3 claims against `S3_COMPAT.md`;
- check format claims against `STATUS.md` / format docs;
- search for stale milestone/development wording when relevant.

Do **not** rerun expensive behavioral/race suites solely because prose,
comments, headings, or source maps changed.

## Public compatibility obligations

If a change affects the ordinary S3 surface, update:

```text
S3_COMPAT.md
```

in the same change.

If it affects the ZeroS3-native protocol, update:

```text
docs/ZEROS3_PROTOCOL.md
```

If it changes persistent formats or compatibility floors, update:

```text
STATUS.md
```

and the relevant format document.

If it changes benchmark claims, record the exact fixture/environment context in:

```text
docs/BENCHMARKS.md
```

Do not document a feature as shipped before executable tests and, where
relevant, black-box interoperability evidence exist.

## Tests

### Root tests

`zeros3_test.go` is stdlib-only white-box validation.

It is the right layer for:

- invariants;
- hostile parsing;
- crash injection;
- corruption;
- deterministic concurrency;
- internal storage transitions.

### External harnesses

`testing-harnesses/` is the right layer for:

- real SDK behavior;
- process lifecycle;
- endpoint compatibility;
- application workflows;
- black-box benchmark evidence.

Do not move third-party clients into the root module.

## Failure-safety expectations

Storage changes should preserve these broad properties:

- no partial object becomes visible;
- acknowledged state respects the documented durability boundary;
- corruption is detected before bad logical bytes are trusted;
- destructive maintenance fails closed when authoritative liveness is uncertain;
- retries after interruption converge;
- immutable old state is removed only after a verified survivor exists where
  required.

Tests should target the actual publication/removal boundaries introduced by the
change rather than creating a redundant giant crash matrix for unrelated code.

## Performance changes

For performance work:

1. identify the actual measured bottleneck first;
2. record a baseline on the same fixture;
3. change one architectural mechanism at a time where practical;
4. measure throughput **and** RSS/file count/I/O amplification when relevant;
5. verify correctness before treating a faster number as a win.

Do not compare absolute numbers from unrelated historical harnesses.

## Commit / PR hygiene

Prefer a small number of coherent commits.

Commit messages should explain the substantive change without generated
session metadata.

Do not add:

- generated task-report files;
- model/session transcripts;
- `Claude-Session` trailers;
- generated attribution/footer noise.

A pull request should state:

- what changed;
- why;
- relevant invariants;
- validation actually run;
- expensive unrelated gates intentionally not rerun.

Keep the PR description concise enough to review.

## Generated/AI-assisted contributions

Tool assistance is fine; the submitted code/docs remain the contributor's
responsibility.

Review generated changes for:

- invented behavior;
- stale flags/constants;
- duplicated mechanisms;
- narrative comments that restate obvious code;
- unnecessary abstraction;
- dependency growth;
- generated attribution/session noise.

Comments should explain non-obvious invariants, protocol behavior, durability,
security, or concurrency—not narrate every line.

## Style

Prefer:

- precise names;
- compact helpers;
- explicit invariants;
- bounded resource use;
- deterministic ordering;
- fail-closed parsing;
- small public surfaces.

Avoid architectural layers that exist only to make the tree look more
conventional.

## Before opening a PR

At minimum:

1. `gofmt` changed Go files;
2. run focused tests for the changed behavior;
3. run `go vet` for any affected module where appropriate;
4. update compatibility/docs in the same change;
5. ensure the root `go.mod` still has no `require` block;
6. run source/test map checks if line movement affects them;
7. make sure the working tree contains no generated logs/reports.

For a docs-only PR, use documentation/static validation rather than expensive
behavioral gates.

## Security issues

Do not publish a sensitive vulnerability report merely to satisfy the normal
issue workflow.

The repository does not yet publish a dedicated private security-reporting
address in documentation. Until one is configured, avoid adding a speculative
`SECURITY.md` that promises a channel the maintainer does not actually monitor.
