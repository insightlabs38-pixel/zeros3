# Reproduce FB-J2 preflight

Use Go 1.27.0 with two Go workers. Tests use disposable local stores,
and the boot transition is modeled. The recorded workspace has a
two-core CPU quota, 8 GiB RAM and overlay storage.

```sh
GOMAXPROCS=2 go test -count=1 -timeout=2m -v \
  -run '^(TestFBJ2IncompleteWatermarkEvidence|TestFB01R(WatermarkEncoding|WatermarkLifecycle|InterruptedGroupMatrix|DegradedViews|TailDiscardInterrupted|WatermarkWriteFailure))$' .
```

Historical performance, durability controls and the original Design B
are in [FB-01R](../FB-01R/REPRODUCE.md).
