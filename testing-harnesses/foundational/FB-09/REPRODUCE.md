# Reproduce FB-09

Use Go 1.27.0 and the checked-in file in `testdata/fb09/`.

```sh
GOMAXPROCS=2 go test -v -count=1 -timeout=100s -run '^TestFB09' .
GOMAXPROCS=2 go test -race -v -count=1 -timeout=100s -run '^TestFB09' .
GOMAXPROCS=2 go test -run '^$' -fuzz '^FuzzFB09BinaryManifest$' \
  -fuzztime=6s -parallel=2 .
```

For independent old-reader refusal, build at the last FB-08
checkpoint and build this tree. Start the new binary with
`serve -binary-manifests` against a disposable store and make one
ordinary signed PUT. Stop the server and run `verify -deep`
against that same directory from both binaries. The older binary
refuses the feature at open. The new binary verifies the exact
mixed store through the existing manifest and pack readers.
