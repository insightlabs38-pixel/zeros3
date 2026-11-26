# Reproduce the FB-04 root checkpoint

Use Go 1.27.0. The tests use disposable local stores and generated
payloads. The group fault hooks count and gate the actual journal
sync path. The signed request travels over a loopback HTTP listener.

```sh
GOMAXPROCS=2 go test -count=1 -timeout=75s -v \
  -run '^(TestFB04DeleteGroup.*|TestDeleteObjects_.*|TestConcurrency_SameKeyPutPutSerializesToOneCompleteVersion)$' .
```

For the engine comparison, build de81adb and 7c73ebc with the same
compiler. Use the same writable filesystem for both stores and test
scratch. Run the 1,000-key request from the existing tests with
`-test.run '^TestDeleteObjects_Table$/^max1000$'`, alternating the
two test executables over three pairs. The per-request logs exclude
fixture setup time. Default guard is v1 lazy.

Run the signed foreground load with the pinned C1 Python environment:

```sh
"$PYTHON" testing-harnesses/foundational/FB-04/delete_qos.py \
  --before "$BEFORE" --after "$AFTER" --work "$SCRATCH/qos"
GOMAXPROCS=2 go test -race -count=1 -timeout=90s -run '^TestFB04' .
```

Use `FB01R_KILL_ROUNDS=1` with `TestFB01RKill` to repeat one random
and twelve controlled process kills per journal mode. Export
`ZEROS3_JOURNAL_FORMAT=j2` only for the grouped replay pass.
