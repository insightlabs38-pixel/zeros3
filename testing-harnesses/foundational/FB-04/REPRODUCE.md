# Reproduce the FB-04 root checkpoint

Use Go 1.27.0. The tests use disposable local stores and generated
payloads. The group fault hooks count and gate the actual journal
sync path. The signed request travels over a loopback HTTP listener.

```sh
GOMAXPROCS=2 go test -count=1 -timeout=75s -v \
  -run '^(TestFB04DeleteGroup.*|TestDeleteObjects_.*|TestConcurrency_SameKeyPutPutSerializesToOneCompleteVersion)$' .
```
