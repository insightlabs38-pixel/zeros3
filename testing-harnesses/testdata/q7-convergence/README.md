Z3-37 research evidence; no persistent format or production prototype.
`measurements.json.gz`: all37 stages, selected reachable candidate per class.
`summary.json.gz`: grouped costs/overlap/range/packing counters, resource evidence,
Q6 frontier equality check and source-frontier hash. Inputs/provenance unchanged
from `../q6-admission`: pinned real public releases/reexports plus native producer
histories. This is not independent corpus breadth; scaled SST and three PyArrow
layouts retain their limitations. All physical costs retain authoritative CDC.

Reproduce in `testing-harnesses/z3` using the verified Go compiler:
`go run ./cmd/z3convergence -module . -public PUBLIC -producer PRODUCER -git GIT -frontier FRONTIER > final.jsonl`
Use Q6 fetch/producer scripts and existing I1C Git corpus to populate those caches.
Capture resource counters separately into resource.json. Put FRONTIER at
WORK/frontier.json.gz, then from repository root:
`python3 testing-harnesses/scripts/q7_summarize.py ROOT WORK OUT`
Transient span and pack proofs authenticate every restored logical component;
original serialized roots are independently proved before observation. No
serialized alternative source is claimed. Cost fields model reference bytes,
exclude common authoritative-root headers, and price data with lab.PackedSize
and64B/object. Span range counters omit additional whole-logical verification;
pack read counters are for newly packed objects; dead-byte scenario retains only
the smallest member per pack. These are not production timing/memory results.
