# Reproduce GF3

Use the GF0 pinned Linux, Go and input setup. The oracle and synthetic
cohort also run without fixture data. The current native single- and
multi-source lanes share XD, with the former limiting candidate bases
to one and the latter admitting up to four. Full archive adds existing
structural and canonical candidates.

```bash
GOMAXPROCS=2 go test -v -count=1 -timeout=3m \
  -run '^TestGF3(NativeDiscoveryOracle|NativePhrasePanel|SmallBankSweep)$' .
```

Collect the JSON after `GF3DISCOVERY`, `GF3PHRASE` and `GF3PRUNE`.
The pinned held-out HTTPX inputs are from
[GF0 reproduction](../GF0/REPRODUCE.md). Physical size includes compacted
native packs, authority metadata, namespace and locator files. The no-bank
subtraction in the report is a counterfactual over the fixed packed
programs. Read timings are ambient in-process probes after reopen.
