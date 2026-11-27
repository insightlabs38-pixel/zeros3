# Reproduce FB-11

Use Go 1.27 and a local disposable store. The physical metadata
scale run uses a deterministic boundary key inside its temp directory.
Normal activation creates a private random key.

```sh
GOMAXPROCS=2 ZEROS3_TEST_PRE_FB11=/path/to/previous-binary \
  go test -run '^TestFB11Snapshot' -v -count=1 -timeout=100s .
GOMAXPROCS=2 ZEROS3_TEST_SNAPSHOT_PAGES_SCALE=1 \
  go test -run '^TestFB11SnapshotPagesPhysicalScale$' -v -count=1 -timeout=170s .
GOMAXPROCS=2 go test -run '^$' -fuzz '^FuzzFB11SnapshotPageIndex$' \
  -fuzztime=20000x -fuzzminimizetime=0 -parallel=1 .
```

On the running store, enable `serve -snapshot-pages` before creating
a snapshot. Run `bundle export` normally to produce the original
portable full bundle. Import into a new store and run the previous
binary's deep verifier there to check the downgrade path. The previous
binary refuses direct access to the page-typed original.

Use `bundle export -changes-only -base-snapshot BASE -snapshot TARGET`
with the usual `-store` and `-out`. Verify the file with
`bundle inspect -verify -base-store PATH`. Use `bundle import
-snapshot-pages` on an offline store to publish the reconstructed
descriptor over shared pages. The default still imports a v1-shaped
descriptor and exports legacy v1 bundles.

```sh
GOMAXPROCS=2 ZEROS3_TEST_PRE_FB11=/path/to/previous-binary \
  go test -run '^(TestFB11|TestBundleDelta_(Import|Export|Parser)|TestFB02_SnapshotSession)' -v .
GOMAXPROCS=2 go test -race -run '^TestFB11' -count=1 -timeout=120s .
GOMAXPROCS=2 go test -run '^$' -fuzz '^FuzzFB11SnapshotDelta$' \
  -fuzztime=22000x -fuzzminimizetime=0 -parallel=1 .
python testing-harnesses/foundational/FB-11/range_proof_cost.py
```

On a disposable offline store, `snapshot-pages-gc -store PATH`
reports the exact candidates. Retry with `-apply` to reclaim those
files and rerun the dry-run to confirm convergence.
