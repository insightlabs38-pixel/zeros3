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
