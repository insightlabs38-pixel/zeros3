# Reproduce FB-06

Use Go 1.27.0 and a local scratch directory. The first tests use
generated mixed records and disposable named packs. They do
not require an external endpoint or a historical test suite.

```sh
GOMAXPROCS=2 go test -v -count=1 -timeout=90s -run '^TestFB06' .
GOMAXPROCS=2 go test -race -v -count=1 -timeout=90s -run '^TestFB06' .
go build -o "$BIN" .
"$BIN" locator-build -store "$STORE"
"$BIN" serve -store "$STORE" -locator-generation
"$BIN" serve -store "$STORE" -locator-tags
```

The first command covers the pre-rename process cut and compares
the old generation byte for byte. The passing opener test reads
every current object body in each fallback and finishes with a
deep offline verification. A namespace-only journal mutation
is visible on the next open with the same pack generation.

Run the scale and paired ingest cases separately from race or
other benchmarking jobs. The full scale case uses about 3 GB
of disposable scratch storage and sampled all profiles through
the normal Store opener, in distinct local processes.

```sh
GOMAXPROCS=2 ZEROS3_FB06_SCALE=16777216 ZEROS3_FB06_INGEST=1 \
  go test -v -count=1 -timeout=150s -run '^TestFB06' .
```

Use overlay-backed `TMPDIR` for the reported experiment. The
advised-cold lane requests `POSIX_FADV_DONTNEED` for the sidecar
using the local Python standard library. It takes no privileged
action and gives no guarantee about hardware or container cache
state. The measured Go heap is post-GC retained heap. Replica and
payload cache pressure require separate measurements.
