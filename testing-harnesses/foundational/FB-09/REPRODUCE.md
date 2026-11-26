# Reproduce FB-09

Use Go 1.27.0 and the checked-in file in `testdata/fb09/`.

```sh
GOMAXPROCS=2 go test -v -count=1 -timeout=100s -run '^TestFB09' .
GOMAXPROCS=2 go test -race -v -count=1 -timeout=100s -run '^TestFB09' .
GOMAXPROCS=2 go test -run '^$' -fuzz '^FuzzFB09BinaryManifest$' \
  -fuzztime=6s -parallel=2 .
GOMAXPROCS=2 go test -run '^$' -fuzz '^FuzzFB09MetadataPage$' \
  -fuzztime=6s -parallel=2 .
GOMAXPROCS=2 ZEROS3_TEST_METADATA_SCALE=1 go test -v -count=1 \
  -timeout=100s -run '^TestFB09MetadataPageScale$' .
```

For independent old-reader refusal, build at the last FB-08
checkpoint and build this tree. Start the new binary with
`serve -binary-manifests` against a disposable store and make one
ordinary signed PUT. Stop the server and run `verify -deep`
against that same directory from both binaries. The older binary
refuses the feature at open. The new binary verifies the exact
mixed store through the existing manifest and pack readers.

On an offline disposable store with the current binary, run
`namespace-checkpoint -store PATH -pages`, then `namespace-pages -store PATH`.
The latter reports the root, row count, page count and physical archive
bytes after verifying them against the primary checkpoint.
