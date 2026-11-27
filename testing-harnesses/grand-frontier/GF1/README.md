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

Next expand the catalog with a candidate that changes read costs
materially. Reconcile predicted and allocated bytes after apply, prune,
GC and repack. Measure retained copies, pack co-residence, read tails,
shared dependency release and exact recovery under an interrupted apply.

[Raw rows](results/native-catalog.jsonl) · [Design](PREREGISTRATION.md) · [Reproduction](REPRODUCE.md)
