# Reproduce FB-15

Use the F6 `corpus/src-series` and `corpus/tree-series` files and the
local Go 1.27 toolchain. Regenerate its source and tool archives for
the selected native trace. The exact local archives are identified
by SHA-256 in the test results. The 18 archived X10 rows came from
`exp/x9-placement/results/real_x10.jsonl` and were used for comparison.

```sh
tar -cf /path/to/tools/gosrc.tar -C /path/to/go src
tar -cf /path/to/tools/gotools.tar -C /path/to/go/pkg tool
GOMAXPROCS=2 ZEROS3_FB15_X9=/path/to/corpus/tree-series \
  ZEROS3_FB15_X10=/path/to/corpus ZEROS3_FB15_TOOLS=/path/to/tools \
  ZEROS3_FB15_SCALE=25000000 go test -v -count=1 -timeout=105s -run '^TestFB15' .
GOMAXPROCS=2 go test -race -v -count=1 -timeout=75s \
  -run '^(TestFB15ExactCandidateModel|TestFB15CheckedJournalSuffix)$' .
```

The trace starts after the first source and toolchain source objects
have been ingested. It replays a source revision at each step, a tree
backup every fourth step, and the tools archive at step 17. The
retention control protects three historical rows per key. The isolated
X9 lane imports changed files and keeps one historical row. Reference
work excludes setup unless stated. The test uses independent canonical
scans to compare the modeled scheduled deletion sets.
