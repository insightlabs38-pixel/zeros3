# Z4 Frontier Campaign ledger

A4 final verdict: **MANAGED11_12_PRODUCTION_READY**; integration through PR #95.
The current task ends with A4. B1–B8 and expansion remain unexecuted by this review.
Hourly automation remains disabled; no new autonomous routine was created.

| Item | Outcome | Integration |
|---|---|---|
| A0 | PRODUCTION_TRANCHE_NEEDS_REWORK; audit and old-retirement evidence | #90 merged |
| A1 | cumulative capability floors; old/new/crash compatibility design | #91 merged |
| A2 | MODEL8_PRODUCTION_READY; bounded exact Model extraction | #92 merged |
| A3 | STRUCTURED9_10_PARTIAL; supported bounded SST/Parquet subset | #93 merged |
| A4 | MANAGED11_12_PRODUCTION_READY; streaming analysis, bounded decode | #95 qualified for normal merge |
| B1 | SIMILARITY_LAYER_READY_FOR_PROTOTYPE; generic similarity delta CAS research | #96 merged |
| B2 | REP_AUTHORITY_VIABLE_FOR_SELECTED_KINDS; floor-10 prototype | #97 draft, unmerged, DRAFT_ONLY |
| B3 | CROSS_FORMAT_CAS_NICHE; typed semantic components, tensor/Git-tar-OCI/columnar lanes | research-only branch `z4/cross-format-semantic-cas` |
| B4 | GRAPH_NORMALIZATION_PARTIAL; exact graph identity + reversible VM + synthesis; LLVM/PDB win, DEX/Wasm do not | #99 (research-only) |
| B5 | Model delta 2.0 | not started |
| B6 | consumer-ranked hard codecs | not started |
| B7 | causal planner under new economics | not started |
| B8 | convergence decision | not started |
| Expansion | only after the main queue | not started |

A4 adds kinds 11 DEX /12 CLR with cumulative store floor 9. Earlier kinds 1–10
retain their decoders. Model kind 8 requires floor 7; SST/Parquet 9/10 floor 8.
Creation remains off by default. Journal/CDC remain authoritative and exact SHA-256
identity is unchanged. Portable full/delta and wire clients retain compatibility;
unsupported old local binaries refuse the capability floor before durable mutation.
No representation-authority or generic similarity production decision was made.

Research lineage remains explicit:
- #80 remains an open, unmerged draft. Its Model subset was integrated by A2;
  managed streaming/production integration is supplied by A4/#95, not wholesale #80.
- #81 remains an open, unmerged draft. A3 integrated its qualified bounded SST/
  Parquet subset; the research branch and original evidence are retained.
- No research branch was deleted and no broad DEFER/KILL conclusion was added.

A4 review repaired component/layout overflow, aggregate signature allocation,
staged truncation/cancellation, shared-body sizing and repeated token validation,
nested group-work accounting and an existing identical-commit fork-resume race.
All final validation, source SHA, native measurements and negative diagnostics:
[Z4_A4_MANAGED_STREAMING.md](Z4_A4_MANAGED_STREAMING.md),
[raw evidence](../z4/a4-integration-evidence.json.gz).
Earlier A0–A3 reports and all A4 checkpoints remain durable; do not restart them.

Post-B8 optimization debts: managed CPU/repeated source inspection, metadata RSS,
full-image private reconstruction/range amplification, optional additive retention,
and A3's expensive exact Zstd paths. Existing managed limits were preserved;
no performance result implies physical storage convergence while CDC is retained.
Next allocation should begin with B1 similarity/delta CAS. B2 remains DRAFT_ONLY.
Do not start B work or re-enable the hourly wake without a new user instruction.

B1 qualified research: [similarity/delta CAS report](Z4_B1_SIMILARITY_DELTA_CAS.md),
[PR #96](https://github.com/insightlabs38-pixel/zeros3/pull/96), verdict
`SIMILARITY_LAYER_READY_FOR_PROTOTYPE`. Exact codecs, bounded discovery, charged
retained closures, 10M-chunk index experiments and structural hybrids are research
only; production source/authority unchanged. B1 does not supersede remaining #80/#81
research. Read amplification, numeric discovery and index lifecycle remain debts.
B2 authority prototype is the recommended next allocation, DRAFT_ONLY; not started.
The hourly routine remains disabled. Stop after B1; B2–B8 require new allocation.

B4 qualified research: [graph normalization report](Z4_B4_GRAPH_NORMALIZATION.md), verdict
`GRAPH_NORMALIZATION_PARTIAL`. Exact SCC-aware graph identities, a bounded reversible transform VM and
cost-driven synthesis beat CDC and B1 on LLVM bitcode (SQLite 6 releases: 0.51x CDC, 0.68x B1) and Portable
PDB (Newtonsoft 5 releases: 0.41x CDC, 0.66x B1) with fresh-process byte-exact replay (26/26 artifacts);
bespoke DEX remains better (0.49 vs 0.70 of CDC), Wasm is a negative control. Research only; production
source, B2 authority and the capability floor are unchanged. Open: module-skeleton ID normalization,
DWARF/ELF adapters, sequence-point line op, piece-indexed range reads. Stop after B4; B5 not started.
