# M2 external interoperability results

**Harness:** `harness/m2` (AWS SDK for Go v2, canonical M2 workflow)
**Tested against zeros3 commit:** `d46e1dece63c8368e84e0b4afe429c903e5fb4cf`
(branch `main`, the verified M2 completion HEAD — merge of
`claude/zeros3-m2-j83lvl` into the former default branch
`claude/zeros3-m0-m1-bootstrap-xvzbbm`)
**Result:** **41 passed, 0 failed**

## SDK default integrity behavior observed

```
SDK default RequestChecksumCalculation=WhenSupported ResponseChecksumValidation=WhenSupported
```

`PutObject` with a seekable body sends an ordinary header-form
`X-Amz-Checksum-Crc32`, included in `SignedHeaders`, no `aws-chunked`
framing. `GetObject` responses carry no checksum, so the SDK logs
`WARN Response has no supported checksum. Not validating response
payload.` and proceeds — this is expected SDK behavior for an S3-compatible
server that does not (in M1/M2 scope) emit response checksums, not a
ZeroS3 defect.

## Coverage

ListBuckets (empty store), CreateBucket, HeadBucket, PutObject (default
SDK checksum path), HeadObject (Content-Length/Content-Type/metadata),
GetObject (byte-exact round trip, Content-Type/metadata), ListObjectsV2
(unfiltered, prefix+delimiter, `max-keys` pagination with no
duplicates/skips), DeleteObject, DeleteBucket, and a full process
restart with persistence verification (large multi-chunk object survives
a `zeros3` restart against the same store directory with byte-identical
`GetObject`).

## How to reproduce

```sh
cd zeros3 && go build -o /tmp/zeros3-bin .
cd testing-harnesses/harness/m2 && ZEROS3_BIN=/tmp/zeros3-bin go run .
```
