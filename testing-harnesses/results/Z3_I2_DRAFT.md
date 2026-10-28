# Z3-I2D — Model/DEX/CLR production draft

Status: **IN_PROGRESS — DRAFT_ONLY; DO NOT MERGE**.
Candidate lab-aligned numbers8/11/12 are draft code, not a frozen contract.
Checkpoint: mechanically extracted bounded managed decoder cores, strict ST/GGUF
header parsers, model/managed recipe adapters. Eight exact lab vectors pass;
all reads are reachable. Root module/dependencies/wire/store/default unchanged.
Managed decoding is capped32MiB output,128MiB total components,1MiB/component,
65536 groups and8KiB/node; claimed output size checked before legacy allocation.
Managed creation is not enabled. Whole-file masked analysis/CLR RSS debt remains.
Streaming Model analyzer now uses256KiB windows, exact explicit-SHA bases with
<=3 operations, header CDC and tensor ranges; five-revision/restart tests pass.
1GiB chunk-distinct sparse control:76.1MiB peak heap growth/97128KiB RSS,
12.2s analyzer+CAS+serialized proof (memory control, not real-model economics).
Managed encoder cores/grouping also ported for offline fidelity; no ordinary
PUT creates managed representations while whole-file materialization remains.
Full compatibility/liveness/fuzz gates and final memory characterization remain.
Continue this draft before moving to Q10; never merge it.
