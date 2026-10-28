Harness-only, gzip-compressed JSON. frontier.json.gz stores actual SHA-addressed
object costs and proved serialized candidate closures, not model weights or
production planner state. results.json.gz contains all base-policy rows and
0/256-byte object-overhead sensitivities. provenance.json.gz pins native input,
archive/member and producer metadata. Capture verified reconstruction and reach.
Cost replay alone is not a new reconstruction of the original files.

Run from testing-harnesses/z3:
  go run ./cmd/z3admission -mode replay -in ../testdata/q6-admission/frontier.json.gz

CLI capture takes explicit -module, -public, -producer and -git cache paths.
Inputs are produced/fetched by scripts/q6_fetch_frontier_inputs.py and
q6_producer_inputs.py; existing Z3-30 SST operation/engine drivers are reused
with the documented scaled constants. No tests download corpora. Merkle case is
synthetic; native producer histories and public releases are labeled separately.
