# Reproduce FB-08

Use Go 1.27.0 with disposable loopback peers. The fixtures use local
test credentials and no external storage.

```sh
GOMAXPROCS=2 go test -count=1 -timeout=110s -v -run '^TestFB08' .
GOMAXPROCS=2 go test -race -count=1 -timeout=110s -v -run '^TestFB08' .
```

The namespace fixture calls the real recursive replication entry
point, counts every HTTP request, checks all destination manifests
and reruns with explicit equivalence handling. The compatibility
fixture advertises legacy discovery and refuses any v3 request.

The stream test uses the actual SigV4 transport and deliberately
ends a source read after the first authenticated payload. The
next signed bitmap and upload prove that only missing bytes are
sent. The independent frame fixture signs malformed logical bodies
and changes a signed transport chunk to test both validation layers.

```sh
GOMAXPROCS=2 go test -run '^$' -fuzz '^FuzzFB08NativeStreamPlan$' \
  -fuzztime=5s -parallel=2 .
```

`TestFB08NativeWireSelection` uses 64 KiB ranges in the current
`zeros3.go` and independently generated entropy. It rotates raw
and selected modes, throttles reads from the signed upload at
10 Mbit/s, measures actual HTTP body bytes, and reads every result
back from the receiver. Run without the race detector and without
competing builds for timing. The high-rate mode uses a 1,000 Mbit/s
hint and an unthrottled loopback socket. Check service times and
payload sizes separately.
