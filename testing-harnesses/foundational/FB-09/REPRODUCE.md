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

For a separate downgrade on a current-only fixture, stop the source
server and run `legacy-copy -store SOURCE -out NEW_DIRECTORY` with
`ZEROS3_JOURNAL_FORMAT` unset. Run the pre-FB09 binary with
`verify -deep -store NEW_DIRECTORY/store`. The focused test exercises
v1 and J2 sources with JSON and binary manifests, checks exact object
and bucket metadata and rejects live sources and retained history.
Provide `ZEROS3_TEST_PRE_FB09=/path/to/pre-FB09-binary` for its
independent refusal and compatibility checks.
