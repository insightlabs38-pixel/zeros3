# FB-15 · Exact incremental maintenance

The Go research planner uses checked journal suffixes to select changed
keys, then loads their exact current and retained manifest roots. It
updates reference counts by immutable manifest identity. Counts nominate
candidates and estimate held bytes. The independent canonical mark
authorizes any deletion.

## Safety

A suffix must match the expected offset, sequence and frame count.
The native reader handles v1 and J2, including compact PUTs. Checked
PUT, delete and prune scopes refresh only affected keys. Unsupported
root operations select a fresh mark. Unknown or damaged state discards
the planner. The native controls truncated, omitted, duplicated,
reordered and damaged actual frames in both formats. A cold reopen
reconstructed both visible objects and rebuilt the lost planner.

A separate graph model includes current, historical, snapshot and
multipart roots and shared depth-ordered dependencies. Eight seeded
histories produced 3,840 exact deletion-set comparisons, 808 malformed
suffix rebuilds, 200 lost-state rebuilds and 200 false zeroes that the
canonical mark vetoed. The comparison walks the graph independently
from the planner's precomputed closures.

## Mark work and held bytes

The policy confirms every four passes or sooner at the 5% held-byte
budget. The described work counts reference visits and updates.
The experiments also ran the independent full scan at every sample
to validate the planner. The modeled mark-work totals charge it only
at a scheduled confirmation.

| Trace | Full-mark work fraction | Peak held share |
|---|---:|---:|
| Reconstructed X10 mix, 31 measured steps | 0.29122 | 2.3945% |
| X9 tree alone, seven measured steps | 0.74655 | 2.6471% |
| Generated 10M roots | 0.25003 | 3.15 MB held |
| Generated 25M roots | 0.25001 | 3.15 MB held |

The native X10 schedule used the 32 archived source revisions, eight
tree backups, and regenerated Go 1.27 source and tool archives at the
original placement steps. Seven scheduled confirmations matched the
canonical deletion sets. The X10 fraction includes 4,197 changed
references and 16,647 confirmation visits out of 71,575 full reference
visits. Including the initial 1,389-reference seed gives 0.311.
Held bytes price stored record extents where available and loose files
elsewhere. The replay did not physically delete the modeled candidates.

The isolated tree has few roots and large changing bodies. It
triggered early full scans and missed the work gate. A scheduler can
fall back when churn consumes a substantial share of the roots.
The dense generated array model reached 25 million roots at about
304 MiB process high-water RSS. It does not qualify a native store
of that size or measure the per-key map's memory use.

**M-G4 is not applicable on this evidence.** A disposable in-memory
plan meets the work and held-byte bounds on the mixed and generated
traces and rebuilds safely from the canonical roots. A persistent
liveness authority would add recovery states without evidence of a
benefit here. The production maintenance path retains its existing
canonical marking and retirement logic.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
