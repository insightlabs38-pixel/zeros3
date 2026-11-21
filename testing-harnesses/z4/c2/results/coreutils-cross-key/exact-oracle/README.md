# Sampled exact candidate oracle

The fixture retains the actual coreutils 9.5, 9.6 and 9.7 tar releases.
Two eligible bases were evaluated for the 9.7 target on a disposable copy
of the verified store. Each base independently encoded a ZW1 candidate
and reproduced the target SHA-256 through the production proof path.
The harness published no new authority record.

The 9.6 base used 2,240,881 encoded piece bytes at depth two. The 9.5
base used 4,578,235 bytes at depth one. Similarity lookup ranked 9.6 first
and 9.5 second. Recall at one and four was one out of one with this
two-base oracle. This compares encoded component sizes for a single
target. It does not establish population recall or charge physical
closure and media overhead. The separate coreutils end-to-end experiment
measures post-maintenance allocation.
