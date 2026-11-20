# P8R production model comparison

Each cohort verifies its fixture hashes against the repository's model pins.
Five Pythia-14m checkpoints or the SmolLM2 f16, imatrix and Q4_0 inputs were
uploaded in their listed order through the archived P8 server. Each side
migrated a separate clone of one compacted CDC store, using Go 1.27.0 and two
logical Go processors. Migration used the default newest order and
`-apply -no-rep -max-read-amp 0 -min-read-ratio 0 -max-depth 2 -max-bases 2 -min-saving 0.05`.
The measured executable identities and hashes accompany the plans.

| Corpus | Seed allocated B | P8 allocated B | P8R allocated B | CPU P8 / P8R s | Peak RSS P8 / P8R KiB |
| --- | ---: | ---: | ---: | ---: | ---: |
| Pythia-14m | 141,193,216 | 124,395,520 | 124,395,520 | 13.11 / 14.75 | 93,732 / 93,936 |
| SmolLM2 Q4_0 | 363,376,640 | 322,469,888 | 322,473,984 | 23.56 / 23.47 | 102,748 / 104,104 |

The four Pythia ZN7 choices and the one SmolLM2 ZN7 choice matched across
both builds. Allocated bytes were measured after GC, repack, compact and
GC. All objects matched their independent SHA-256 hashes through fresh
S3 processes, and both stores in each cohort passed deep verification.
Each comparison is one cohort under ongoing host work. The CPU and RSS
samples describe these runs. Mixed IQ variants, additional model sizes
and dependency pruning are outside these two comparisons.
