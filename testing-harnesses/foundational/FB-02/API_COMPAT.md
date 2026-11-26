# FB-02 API and compatibility notes

No on-disk format changes: journal, manifests, packs and snapshot descriptors
are byte-for-byte what FB-01R writes and reads. Checksum persistence,
replication defaults and protocol v3 are untouched.

## S3 surface (M-A3, M-A4)

- `x-amz-checksum-{crc32,crc32c,crc64nvme,sha1,sha256}` headers, the matching
  aws-chunked trailers (signed and unsigned) and
  `x-amz-checksum-type: FULL_OBJECT` on CompleteMultipartUpload are verified.
  A mismatch is `400 BadDigest` and nothing is published. A COMPOSITE claim on
  Complete is `501 NotImplemented`. Values are checked, not stored or
  returned (ADR-ID-03, owner).
- Per-part `Checksum*` elements in the Complete XML body are ignored, as
  before.
- A DeleteObjects body is checked against any checksum header it carries.
- The ZeroS3 bulk transfer endpoints refuse `UNSIGNED-PAYLOAD`, the unsigned
  trailer mode and presigned URLs with `403 SignedPayloadRequired`
  (ADR-XFER-02); the built-in client always signs them.
- UploadPart and UploadPartCopy refuse, with `400 EntityTooLarge` and no
  state change, a part whose chunk list would not fit one journal frame
  (an 8 MiB record, 90,198 chunks: about 1.4 GiB of content cut at the
  16 KiB minimum chunk). Parts under the cap behave as before.
- Peer responses read by replicate, fork, diff and restore are bounded
  (64 MiB JSON by default, a chunk at most `maxSyncChunkBytes`, snapshot show
  at 4x the snapshot payload cap).

## Sync protocol v1 (M-A5)

Additive fields; old peers ignore them and keep their old behaviour.

- Describe responses (`/_zeros3/v1/object`, `/_zeros3/v1/snapshot/object`)
  carry `object_sha256` again.
- `POST /_zeros3/v1/commit` takes `"if_unchanged": true`: when the current
  version is equivalent (object SHA-256, size, content type, user metadata),
  the server answers 200 with `"unchanged": true` and writes nothing, before
  any manifest or fsync. `/_zeros3/v1/info` advertises
  `"commit_if_unchanged": true`. Authority import was already idempotent and
  now checks before writing.
- `zeros3 replicate -if-unchanged` (single and recursive) asks for that mode
  and skips an equivalent destination without negotiating. Without the flag a
  rerun still republishes (ADR-POL-01, owner).
- Fork and restore stay create-only, but compare by identity, never by ETag:
  an ETag-equal destination with other content is a conflict, and one whose
  identity is unknown goes to the server as expect-absent plus if-unchanged.
- An old destination ignores `if_unchanged` and republishes; an old source
  without `object_sha256` makes identity unknown, so nothing is skipped.

## Snapshot restore sessions (M-A6)

- `POST /_zeros3/v1/snapshot/session?id=` decodes a snapshot once and returns
  `{"session", "entries"}`; `DELETE ?session=` closes it; snapshot describe
  takes `session=`. See `docs/ZEROS3_PROTOCOL.md`, Restore sessions.
- At most 4 sessions and 256 MiB of index per server, 15 minutes idle. A
  full table answers 503; an unknown or dropped session is a full read. A
  snapshot file whose size or modification time changed drops its sessions.
- `snapshot restore` opens one session per run, best effort, and closes it
  at the end. Against a server without the endpoint it restores as before.
- Within a session the answers are those validated when it opened: damage to
  the descriptor that keeps its size and modification time (bit rot) is not
  seen until the session ends, where a sessionless restore would fail the
  remaining objects. Each manifest is still checked against its recorded
  SHA-256 on every describe.
