# Reproduce GF4

Run the optimum check and the generated workload without fixtures. The
native replay uses GF0's two public TypeScript gzip releases, an exact
current archive, signed local requests, and an isolated partially live pack.
Use the two-core quota and pinned Go of [GF0](../GF0/REPRODUCE.md).

```bash
GOMAXPROCS=2 go test -v -count=1 -timeout=4m \
  -run '^TestGF4(NativePolicyReplay|OfflineOptimality|WorkloadReplay)$' .
```

Save lines that start with `GF4` and `GF4NATIVE`. Native lane order is
reversed on the second round. The portable data manifest and checkout
come from GF0. Native copy build time and source charge are in the rows.

Prepare the immutable fixture once outside instrumentation when running
the focused native race test. Reanalyzing the seed under the race detector
exhausted the initial three-minute limit before the concurrent phase.

```bash
export ZEROS3_GF4_SEED=/path/to/new/research-seed
GOMAXPROCS=2 go test -run '^TestGF4NativePolicyReplay$/^prepare-only$' .
GOMAXPROCS=2 go test -race -count=1 -timeout=2m \
  -run '^TestGF4(NativePolicyReplay|OfflineOptimality)$/^0$/^rent-aware$' .
```
