# Bounded authority portfolio comparisons

Three small comparisons cover the planner on the recorded research revision.
The synthetic Go test matches a 512 KiB target to complementary halves in
two bases. The single-base and two-base candidate charges were 264,166 and
1,795 bytes. After reclamation, the stores occupied 1,318,839 and 1,056,469
apparent bytes. The current revision read back exactly and both retained
histories passed deep verification.

The real cohorts used five shipped Newtonsoft PDBs and six [NYC TLC Snappy Parquet derivatives](../../../c5/results/columnar-nyc/README.md).
Both come from earlier pinned corpora. Within each cohort, independent
store clones used distinct keys and the same verified similarity index. The
delta lanes used archive policy and disabled ZN7 and structural forms. The
mixed Parquet lane allowed structural and delta candidates. Each lane was
maintained with GC, repack, compact and GC. A clone of each maintained
lane also ran explicit `repack -recompress -apply`.

| Cohort | Policy | Maintained allocated bytes | After explicit recompression |
| --- | --- | ---: | ---: |
| PDB | CDC | 1,691,648 | 966,656 |
| PDB | One or two bases | 851,968 | 851,968 |
| Parquet | CDC | 7,372,800 | 6,766,592 |
| Parquet | One or two bases | 6,615,040 | 6,168,576 |
| Parquet | Structural or mixed | 17,752,064 | 3,944,448 |

The real delta lanes admitted the same forms and retained sizes at both
base limits. Their plans do not establish a real-corpus multi-base win.
The mixed lane admitted structural Parquet forms. Its projected economics
require recompression of fully live raw packs, which ordinary repack
leaves in place. Both maintenance states are useful results. Single shared-
host timing samples are recorded with the earlier heuristic planner for
comparison. Fresh signed GETs and 4 KiB range digests matched the inputs
in every maintained lane. Both maintenance states passed deep verification.

The subsequent structural accounting regression covers a component
owned only by CDC roots in the candidate group. Retaining that component
charges its payload and pack framing to the replacement. A component with
an independent live owner adds no duplicate charge. The planner evaluates
that ownership against the members that actually built, including their
record metadata.

[Trial reports and hashes](trials.json)
