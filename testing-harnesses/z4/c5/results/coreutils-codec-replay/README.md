# Opt-in archive gzip and zstd replay

`authority migrate -archive -codec-replay` groups gzip and zstd
containers so they can share decoded components. The same recipe
formats already supported for nested objects are used for authority.
The default does not consider top-level replay, and hot and warm
policies exclude it unless the explicit read-cost override is used.
Temporary plaintext and zstd unit writes honor the migration reserve.

Four differently compressed versions of the same pinned Coreutils
9.7 tar were uploaded through signed S3. Each lane used the same GC,
repack, compact and recompression sequence.

| Lane | Retained allocated bytes | Authority records |
| --- | ---: | ---: |
| CDC | 47,120,384 | 0 |
| Delta | 47,120,384 | 0 |
| Codec replay | 30,748,672 | 3 |
| Codec replay and delta | 30,748,672 | 3 |

All sixteen whole and midpoint 4 KiB signed reads matched independently
hashed fixture bytes. All four stores passed deep verification. Three
containers used exact replay and one remained CDC. The two migrations
with codec replay took 105.5 and 131.8 seconds under shared load. The
first object in the replay-only lane needed about 11.4 seconds for a
whole signed read and 9.5 seconds for its first midpoint range.
The comparable CDC reads took 163 ms and 8 ms. These are individual
shared-host samples. First-range work still includes full replay for
these containers, which limits the opt-in policy to cold data.

[Fixtures, plans and checks](trials.json)
