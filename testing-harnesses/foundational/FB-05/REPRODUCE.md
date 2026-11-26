# Reproduce FB-05 byte admission

Use Go 1.27.0. The physical fixture uses generated unique 8 KiB
objects and a 48 KiB compaction target so several packs have
independently deletable records. Data is hashed and verified
after reopen. The online pass uses both a one-byte-short budget
and an exactly fitting budget.

```sh
GOMAXPROCS=2 go test -count=1 -timeout=90s -v \
  -run '^(TestFB05RewriteBudgetRetiresDeadSeparately|TestFB03_MaintIsolatesCorruptRecord|TestB4OnlineReclaimsDeadData|TestB4CancelledPass)$' .
```

To check screened compression, run `TestFB05RawCompressionAdmission`
with the same compiler. The positive source uses distinct numbered
text, the control uses deterministic random bytes, and both start
with all-raw packs. Run both passes and verify from a fresh process.
The positive source also tests an insufficient aggregate scratch
budget followed by verification, reopen and a successful retry.

`TestFB05MultipartExpiryScopeAndRetry` journals fixed initiation
times, verifies a recent part, injects sync ENOSPC and retries
after reopening. The fixture uses both journal formats. The
bucket, prefix, UTC cutoff and default-off controls are verified
independently.

## Rebuild the X9 versioned corpus

Extract only `F6-zeros3-audit/zeros3-f6-evidence.tar.gz` from the
research ZIP, then select `corpus/SHA256SUMS`, `exp/x9-placement/src/`
and the X9 `results/trace_*.gz` members from that tarball. In the
following commands, `X9` is the extracted placement directory and
`CORPUS` is its sibling research corpus directory. Use a checkout
with the audited ZeroS3 Git history.

```sh
mkdir -p "$CORPUS/src-series" "$CORPUS/tree-series"
while read -r digest location; do
  rel=${location#*/corpus/}
  file=${rel##*/}
  commit=${file#*_}
  commit=${commit%.*}
  case "$rel" in
    src-series/*) git show "$commit:zeros3.go" > "$CORPUS/$rel" ;;
    tree-series/*) git archive --format=tar "$commit" -- . ':!testing-harnesses' > "$CORPUS/$rel" ;;
    *) continue ;;
  esac
  printf '%s  %s\n' "$digest" "$CORPUS/$rel"
done < "$CORPUS/SHA256SUMS" | sha256sum --check
```

## Native and model comparisons

The audited source and tree series have 40 verified files. The
X9 versioned-series selector excludes the two one-off toolchain
objects on both sides. For the engine use the actual native
direct ingest, retention, locator, mark and replacement paths.
The test compares each final live object SHA-256 and then runs
deep reachability verification after recompression.

```sh
GOMAXPROCS=2 ZEROS3_FB05_X9_CORPUS="$CORPUS" ZEROS3_FB05_X9_BUDGET_MIB=4 \
  go test -v -timeout=120s -run '^TestFB05ReplayX9$' .
GOMAXPROCS=2 ZEROS3_FB05_MIXED_CORPUS="$C14_CORPUS" \
  go test -v -timeout=120s -run '^TestFB05MixedCorpusDebt$' .
patch -d "$X9/src" -p1 < testing-harnesses/foundational/FB-05/x9-byte-budget.patch
(cd "$X9/src" && go build -o ../x9 .)
"$X9/x9" sim -results "$X9/results" -workload real -series-only -raw \
  -policy request -ret R1 -target-mib 4 -budget-mib 4 -check-every 1 -j 1
"$X9/x9" sim -results "$X9/results" -workload synth -hot .01 \
  -n 100000 -steps 36 -warmup 8 -seed 1 -policy request,reqage,reqpredict \
  -ret R1 -target-mib 64 -threshold .40,.45 -budget-mib 640 -j 2 \
  -check-every 4 -loc-every 0
```

Repeat the synthetic comparison for seeds 2 and 3. The results
include the smaller 20,000-object budget and threshold sweeps.
Use an overlay-backed `TMPDIR` for native measurements. The
campaign machine had a two-CPU quota and Go 1.27.0.

## Signed foreground comparison

With C1's Boto3 environment, build the server and run
`recompress_qos.py --binary "$BIN" --corpus "$CORPUS" --work "$SCRATCH"`.
Candidate and idle intervals are aligned by each completed pass's
start and end relative to their identical request schedules.
The raw rows contain issue lateness and service time in
milliseconds. The duty lane uses adaptive yielding and the
`--duty` wall-time target. Run with no competing test jobs.
The complete-window comparison used
`--pairs 8 --modes idle duty --slots 600 --duty 20`.
Summarize the compressed logs with `python3 qos_summary.py results/qos*.gz`.
The older pilots are retained with their truncated-window counts.
