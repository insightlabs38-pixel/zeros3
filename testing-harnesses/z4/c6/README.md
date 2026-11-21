# Static admission screen

The historical replay compares four published fixed rules on matched
archived workloads. Each price book picks a rule using four labeled folds
and then scores it on the held-out fold. The screen compares the policy
with the best recorded run and with B7's exact hindsight where the
archive has one. The archived workloads are a replay study. Their access
rates and modeled costs are distinct from the observed store trials.

The matched replay selected the same premium cap on both price books.
In the storage-weighted book, holdout cost ranged from 0.675× to 1.065×
CDC. For the I/O-heavy book, it ranged from 0.899× to 1.328× CDC.
Exact hindsight was available for ten matched rows across the two books.
The model fold had no exact hindsight row in this matched subset.

The independent cold-tier controls use actual retained bytes and
independently verified readback. The mixed history and Brotli controls
expanded beyond CDC. Native Coreutils relay reduced retained bytes by
10.11% in its narrower fixture, with whole-read throughput still
unmeasured in that relay. All three defaults stayed CDC. The same
measured subset is insufficient to price field access rates,
shared-base repair and all peak migration costs. The relay case shares
a family with the mixed fixture and is excluded from any claim about
independent folds.

`evaluate.py` takes the five input artifact paths and writes a JSON
report that includes their SHA-256 digests, per-fold replay costs and
measured gaps. It uses a fixed 10% retained-byte gate and a 0.5×
whole-read gate for the experimental cold-tier comparison.

[Fixed-policy comparisons and source identities](results/fixed-tier-screen.json)
