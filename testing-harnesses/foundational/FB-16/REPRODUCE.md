# Reproduce FB-16

Both current servers can enable `serve -namespace-projection`.
An incremental additive run can fall back to the existing enumeration
on an older peer. Separate explicit mirror requires
`serve -projection-mirror` on the destination.

```sh
zeros3 replicate -recursive -incremental -protocol3 -dry-run \
  -from http://127.0.0.1:9000 -to http://127.0.0.1:9001 s3://source/prefix s3://destination/prefix
ZEROS3_FB16_SCALE=1000000 GOMAXPROCS=2 go test -v -count=1 -timeout=100s -run '^TestFB16' .
GOMAXPROCS=2 go test -race -count=1 -timeout=100s \
  -run '^(TestFB16ProjectionRandomized|TestFB16ProjectionShortHintCollision|TestFB16SignedProjection|TestFB16ProjectionTransport)' .
GOMAXPROCS=2 go test -run '^$' -fuzz '^FuzzFB16ProjectionQuery$' \
  -fuzztime=24000x -fuzzminimizetime=0 -parallel=2 .
```

For actual older-peer compatibility, build `zeros3.go` from 6762d03
and set `ZEROS3_TEST_PRE_FB16` to that binary when running the suite.
The final union also uses the pre-FB09 and pre-FB10 builds at
1113d73 and f4364aa, and the saved pre-part-page and pre-FB11
binaries cited by their batches. Their checksums appear in the results.

The one-million-row test keeps generated metadata and two projection
trees in memory. The wire figures include application bodies and their
authentication tags. The 144-object score run uses signed HTTP with
actual payloads and validates retained bytes by reading every object and
deep-verifying the destination.
