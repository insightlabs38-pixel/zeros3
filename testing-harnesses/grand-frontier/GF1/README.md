# GF1 · constrained shared-reconstruction selection

The first experiment uses current native candidate generation, a fixed
catalog of depth-one programs, and a bounded exact oracle. The physical
integration and joint placement problem are open.

## Formulation and restricted proof

For root `i`, choose one exact program `x_i` from its catalog. Let
`S_i(x_i)` be its full immutable closure of content records, pins and
authority metadata. Each record has a packed length including the
per-record index and a fixed pack assignment. Let `U` be the union of
the retained roots' closures. For each pack with any member of `U`, add
the packed record lengths and pack header, then round up to quantum `q`.
Also enforce each root's depth and whole/range work caps, each pack's
capacity and the sum of writes. A shared parent persists after pruning
its manifest whenever any retained root still requires it. The native
catalog fixes a direct parent before its children, making cycles
unrepresentable. General parent choice will need a DAG and depth check.

For a fixed partial assignment, any extension adds only nonnegative
costs to the union. Rounded pack costs, writes and limits are monotone.
The cost at an internal node lower-bounds every feasible descendant.
The exact oracle prunes against a feasible incumbent using this bound
and reports whether it exhausted its node cap. Cohort exchange accepts
only strictly smaller feasible assignments. Prefix beam stops at a
fixed width and provides a feasible upper bound.

The counterexample uses two 8-byte roots, each in a distinct direct pack.
A shared alternative costs 8 bytes for the base plus 2 bytes per root
in the same pack, rounded to a four-byte quantum. Either root alone
costs 12 versus 8, so independent selection keeps 16. The joint optimum
costs 12. The read, write, capacity and depth ablations force the oracle
back to 16. Removing all referring roots frees the shared charge.

## Native input costs

At a 2× per-root decoded-work cap, results in bytes in the restricted
pack model are as follows. Both searches matched the exact oracle.
Independent and single-base agree in this one-parent catalog.

| Family | Independent | Exact, cohort and beam | Current archive, actual GF0 allocation |
| --- | ---: | ---: | ---: |
| TypeScript tar | 9,158,656 | 4,714,496 | 4,792,320 |
| SQLite | 626,688 | 532,480 | 319,488 |
| Parquet | 1,224,704 | 991,232 | 634,880 |
| Sparse volume | 380,928 | 339,968 | 319,488 |
| Independent entropy | 8,404,992 | 8,404,992 | 8,433,664 |

This is a restricted catalog and a pack cost model. It charges native
record prices, modeled grouped metadata, and two fixed pack allocations.
The actual GF0 lane may choose different parents, depths and codecs,
and includes native manifest, journal and locator files. Matching-domain
repricing of the unrestricted native delta solve succeeded on the
entropy control. Its other choices required different depth or parents.
The table gives no evidence of better physical storage than the current
archive planner. It does establish the cost of obtaining stricter
guarantees in this catalog. For TypeScript tar under the 1×, 2× and 8×
work limits the oracle gives 4,964,352, 4,714,496 and 4,698,112 bytes.

## Native physical revision

Add compiled compressed programs as options over independently verified
CDC records. Form the union even when the anchor changes its own
representation. Two adjacent output frames upper-bound every 4 KiB
request for this codec, including authenticated source chunks, programs
and recipe. The planner independently replays every frame before solving.
Native deep verification and signed range replay follow publication.

| TypeScript gzip v0–v1 | Maximum decoded record bytes, target | Modeled bytes | Actual allocated bytes |
| --- | ---: | ---: | ---: |
| Strict 1× CDC cap | 284,398 | 8,364,032 | 8,392,704 |
| Loose 8× cap, compiled target | 677,488 | 5,382,144 | 5,414,912 |

The modeled savings were 2,981,888 bytes. Actual savings were 2,977,792,
within one 4 KiB allocation block. Signed range p95 was 0.30 ms for CDC and
0.96 ms for the compiled plan over the same roots. A real closed plan can
meet a hard work cap while leaving latency sensitive to contention and
placement. The canonical archive is still the minimum-space lane.
[Signed native comparison](../GF2/README.md#signed-native-authority)

The held-out urllib3 loose cap selected a marginal patch, saving only
4,096 bytes while sparse p95 grew from 0.30 to 1.91 ms. Hard decoded work
alone admits undesirable tail behavior. Keep this as a negative decision
control for workload-aware admission. Plan size is modeled on two pack
classes, so this does not establish dynamic layout optimality. Native
prune/recovery for the selected codec and the shared phrase closure are
exercised in GF2 and GF3. Mixed-load selection remains an integration gate.

[Raw rows](results/native-catalog.jsonl) · [Design](PREREGISTRATION.md) · [Reproduction](REPRODUCE.md)
