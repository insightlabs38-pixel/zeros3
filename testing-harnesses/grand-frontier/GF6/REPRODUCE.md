# Reproduce GF6

All fixtures are deterministic and local. The two signed loopback
services use disposable repository test identities. No network shaping
privileges or external accounts are needed.

```bash
GOMAXPROCS=2 go test -v -count=1 -timeout=2m \
  -run '^TestGF6(SignedNegotiation|ShapedSignedNegotiation)$' .
```

Extract rows prefixed with `GF6`. The paced lane charges both request
and response bodies on the source and destination clients. Prepared
closure timings also give the amortizable source setup as a separate field.
