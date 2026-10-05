# Security Policy

ZeroS3 is a pre-1.0, single-node, self-hosted S3-compatible object store. This
policy explains how to report vulnerabilities privately and what security
boundary the current project actually claims.

## Reporting a vulnerability

Use GitHub Private Vulnerability Reporting for this repository:

1. open the repository's **Security** tab;
2. choose **Report a vulnerability**;
3. submit the report privately.

Do **not** open a public GitHub issue for a vulnerability that could put users
or deployments at risk before a fix or disclosure plan exists.

A useful report includes, where applicable:

- affected ZeroS3 version, tag, or commit;
- deployment assumptions needed to reproduce the issue;
- the relevant S3 or ZeroS3-native request/operation;
- minimal reproduction steps or a proof of concept;
- expected versus observed behavior;
- likely impact and attacker prerequisites;
- whether the issue crosses an authentication, integrity, confidentiality,
  filesystem, or durability boundary.

Do not include real credentials, private user data, or production secrets when
a synthetic reproduction can demonstrate the issue.

## Supported versions

Because ZeroS3 is pre-1.0, security fixes target:

- the latest published release; and
- current `main` when the fix has not yet been released.

Older pre-1.0 releases are not guaranteed to receive backported fixes. When a
security fix ships, upgrading to the newest release may be the supported
remediation.

The current maturity and compatibility policy is documented in
[STATUS.md](./STATUS.md).

## Security boundary

The current ZeroS3 security model includes:

- SigV4 request authentication;
- one configured static access-key/secret pair per server;
- optional TLS through Go's HTTP/TLS stack;
- SHA-256 verification of logical chunk content;
- bounded parsing for storage/network artifact formats;
- fail-closed handling of unsupported persistent/protocol versions;
- no server-side peer discovery or arbitrary background peer fetching.

The current project does **not** provide:

- IAM or STS;
- per-user/per-tenant authorization;
- bucket policies or ACLs;
- KMS or server-side object encryption;
- distributed consensus or multi-node isolation;
- a hardened public multi-tenant cloud-service boundary.

Those absences are product boundaries, not vulnerabilities by themselves.

See [S3_COMPAT.md](./S3_COMPAT.md) and
[docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) for the documented contracts.

## High-value report classes

Examples of reports that are especially relevant include:

- SigV4 authentication bypass or request-canonicalization confusion;
- unauthorized access across the documented bucket/object boundary;
- path traversal or arbitrary filesystem read/write/delete;
- unsafe parsing that permits memory/CPU exhaustion beyond documented bounds;
- malicious bundle, journal, pack, snapshot, or protocol input causing silent
  integrity failure;
- a corruption path that serves bytes without the required logical digest
  verification;
- destructive GC/repack/tier behavior that can delete live content despite
  valid authoritative roots;
- credential or secret disclosure through logs, responses, or generated URLs;
- unexpected outbound network access or SSRF from server-side behavior;
- an acknowledged mutation violating the documented durability/visibility
  boundary in a way that can cause silent data loss.

Reports involving a local attacker who already has arbitrary write access to
the entire store directory, can replace the ZeroS3 binary, or controls the host
kernel are generally outside the remote application security boundary.
However, corrupted on-disk input that triggers an additional unsafe behavior
may still be security-relevant and is worth reporting.

## Coordinated disclosure

Please give the maintainer a reasonable opportunity to reproduce, fix, and
prepare a release before publishing exploit details for a vulnerability that
could affect users.

No fixed response or remediation SLA is promised for the pre-1.0 project.
Reports will be triaged according to reproducibility, impact, attacker
requirements, and affected security boundary.

When appropriate, fixes may be coordinated through a GitHub Security Advisory
and released before public technical details are disclosed.

## Non-sensitive bugs

Ordinary correctness, compatibility, documentation, or performance bugs that do
not create a security risk should use the normal public issue/PR workflow.

## Related documentation

- [STATUS.md](./STATUS.md): maturity and supported deployment boundary
- [S3_COMPAT.md](./S3_COMPAT.md): exact ordinary-S3 contract
- [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md): integrity/durability model
- [docs/OPERATIONS.md](./docs/OPERATIONS.md): deployment and recovery guidance
- [CONTRIBUTING.md](./CONTRIBUTING.md): contribution workflow