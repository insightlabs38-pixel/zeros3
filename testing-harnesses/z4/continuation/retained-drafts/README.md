# Historical research drafts

The exact draft patches and base/head commits retain the implementations and their original fixtures, tests, and evidence. Recreate a draft with `git worktree add --detach PATH HEAD`, using the head in [refs.tsv](refs.tsv). The patch is also directly applicable to the listed base. The draft heads are parents in research history.

| Draft | Current implementation | Remaining integration path |
|---|---|---|
| I2 model and managed files, #80 | Production Model planes, bounded basis reads, and streaming DEX/CLR ports | Use the draft managed partitioning and metadata-slot probes to price identity sharing on shipped files. Its monolithic managed parser and non-streaming payload assembly must be adapted to current bounded readers. |
| I3 Parquet and SST, #81 | Production Parquet and SST decoders, including later hard-codec versions | Use the draft GiB/range harness as an independent comparator. Translate its earlier recipe limits and knobs to current page-range routing before reusing the assertions. |
| B2 authority, #97 | C1 body records, retained dependency accounting, revert, and the integrated C3 transfer and repair protocol | Reuse the draft economics comparator with current physical accounting and recovery rules. The old envelope matcher and publisher are superseded by the current windowed matcher, validation and maintenance paths. |
| P1B durability | Current group commit, direct packs, publication barriers, and P1C segments | Reuse the historical ordered-write model and syscall accounting when extending P9 epoch/online maintenance. The draft benchmark assumes the older pack layout. |
