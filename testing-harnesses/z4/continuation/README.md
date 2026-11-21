# ZeroS3 research development

`research/next` is the long-lived development branch. `main` is the stable
public pre-1.0 baseline. Commit coherent work directly to the research branch
and push it at engineering checkpoints.

The [ledger](QUEUE.json) retains the original 44 items and their historical
evidence. The `research` fields govern current scheduling.

| Milestone | Outcome |
| --- | --- |
| R0 | Unified development tree, including the retained draft ancestry and [original patches](retained-drafts/README.md). |
| R1 | Persistent, bounded cross-history discovery and exact, physically priced representation selection. |
| R2 | Negotiated native closure transfer, durable resumption and digest-verified repair. |
| R3 | Exact representations with measured storage and read costs. |
| R4 | Durable namespace, recovery and lower-interruption maintenance. |

Run focused exactness, compatibility and fault tests as the code changes.
Regenerate both checked source maps with `go generate .`. Preserve failures
and competing results. Full qualification and large release campaigns are
deferred to a release decision. The Go P3 investigation remains paused.

The [index contract](../c2/INDEX_CONTRACT.md) describes staged publication,
the scoped planner and recovery. The previous C2A qualification ended at the
transition after 34 passing mandatory stages and one interrupted stage.
[Recorded stages](../c2/results/qualification/transition-stages.tsv)
