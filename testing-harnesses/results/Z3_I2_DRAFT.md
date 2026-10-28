# Z3-I2D — Model/DEX/CLR production draft

Status: **IN_PROGRESS — DRAFT_ONLY; DO NOT MERGE**.
Candidate lab-aligned numbers8/11/12 are draft code, not a frozen contract.
Checkpoint: mechanically extracted bounded managed decoder cores, strict ST/GGUF
header parsers, model/managed recipe adapters. Eight exact lab vectors pass;
all reads are reachable. Root module/dependencies/wire/store/default unchanged.
Managed decoding is capped32MiB output,128MiB total components,1MiB/component,
65536 groups and8KiB/node; claimed output size checked before legacy allocation.
Managed creation is not enabled. Whole-file masked analysis/CLR RSS debt remains.
Streaming model analyzer, base liveness/hop/range tests and compatibility matrix
are unfinished. Continue this draft before moving to Q10; never merge it.
