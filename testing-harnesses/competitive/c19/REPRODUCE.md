# Reproduce C19

Use a disposable Linux machine with two CPU cores of quota and the pinned
Go 1.27.0 toolchain. The reference run used amd64, an 8 GiB memory limit,
and local overlay storage. Hold other workloads idle during each timing
phase. The signed servers bind to loopback with disposable credentials.
The measured C19 source is 519f53b and C9 is 3cf73e3. Build both from
their respective clean worktrees using `go build -trimpath -o OUTPUT .`.
Keep the files named `zeros3-integrated` and `zeros3-c9` in scratch, or
set `C19_INTEGRATED` and `C19_C9`.

Obtain the four public held-out assets and the two TypeScript releases
at the URLs in [inputs.json](inputs.json). Point `C19_INPUTS` to the
held-out files and `C19_DOWNLOADS` at the TypeScript downloads. The
retention driver verifies each pin before ingest. Chinook's released
files have the same digest. Its third fixture is a local copy with
the first 12 Track names suffixed by ` (C19)`. Use SQLite's ordinary
commit mode. The raw fixture rows carry the post-edit digest and semantic
proof. See [C14 reproduction](../c14/REPRODUCE.md) for the other frozen
histories, ext4 tooling, sample checks and fixture manifest. C1 provides
the deterministic shift/entropy pair, boto3 and snapshot binaries.
[Tool hashes](tools.sha256) cover the tested engines and codec binaries.

Use the C1 Python environment with boto3 1.44.11, hf-xet 1.8.0, pyarrow
18.1.0 and rocksdict 0.3.29. Point `C19_COMPETITORS` to C1's directory
holding `bin/` and `C19_GO` to the pinned compiler. The external artifacts
match C1's SHA-256 locks. The relevant pins are MinIO 07c3a42, RustFS
1.0.6, Restic 0.20.0, Kopia 0.23.1 and Borg 1.4.5. OpenXet's pinned
C1 build reports 0.0.0. The independent codecs are xdelta3 3.0.11 and
zstd 1.5.7. Defaults for the reused
C1/C2/C12/C14 scratch inputs sit under `C19_SHARED`, with individual
overrides in the script. The compatibility fixture comes from C18's
successfully repaired native preset store, reproducible with its paired
repair driver.

```bash
export C19_PYTHON=/path/to/c1/venv/bin/python C19_GO=/path/to/go
for step in general retention transport qos backup codec openxet compatibility acceptance; do
  ./replay.sh "$WORK" "$step"
done
C19_MERGED_WORKTREE=/path/to/1d4f36a-worktree ./replay.sh "$WORK" merged
"$C19_PYTHON" summary.py "$WORK" > "$WORK/summary.jsonl"
```

The finalization sequence is identical across retention lanes. Compare
the total physical bytes before snapshots. The integrated lane also
exports one portable bundle per history and verifies a fresh C9 restore.
The volume trials remove that export snapshot before physical pruning.
Run the phases serially. Two paired repetitions reverse trial order.
Small transport controls and each portable recovery run once and are
reported accordingly. Latency ranges describe the trials, not confidence
intervals. The transport relay counts both directions on both HTTP legs
with local pacing and configured delay per request. Local OpenXet reports
interface counters for its distinct client and repository protocol.

Run `acceptance.sh` against the frozen code. Its selected and
unselected catalogs make the bounded root coverage explicit. For
the concurrent FB-02 merge, check out 1d4f36a in a second worktree
and run `C19_MERGED_WORKTREE=PATH ./replay.sh "$WORK" merged`.
That phase repeats two native transfer cases and tests the affected
protocol and publication boundaries. Runtime resources, digests and
operational output remain in the accompanying raw JSONL. The landing
run repeats focused tests at the branch tip recorded in
[landing/source.txt](results/landing/source.txt). Run
`./landing-check.sh /path/to/landing-worktree "$WORK/landing"` there
to reproduce the restore-session, pack locator, and frontier checks.

An initial two-minute run reached the historical B8 crash matrix timeout.
The final bounded partition omits that matrix and repeats the applicable
actual cuts for archive finalization, import, dictionary publication and
repair. See the selected catalog for the entire executed scope.

A three-minute race run of historical B8 concurrent reads reached the
cap. The focused root race selection is in `acceptance.sh`. Run the
reader-only C10 check against the finalized C19 urllib3 lane as follows.

```bash
root=$(git rev-parse --show-toplevel)
mkdir -p "$WORK/reader-race"
cp "$root"/{zeros3.go,go.mod} "$WORK/reader-race/"
cp ../c10/profile_test.go.txt "$WORK/reader-race/read_test.go"
(cd "$WORK/reader-race" &&
  C10_STORE="$WORK/retention/urllib3-0-integrated/store" \
  C10_KEY=0/urllib3-2.2.2.tar.gz \
  C10_SOURCE="${C19_INPUTS:-$WORK/inputs}/urllib3-2.2.2.tar.gz" \
  ZEROS3_GZIP_SEQUENTIAL=1 GOMAXPROCS=2   "$C19_GO" test -race -count=1 -timeout=45s -v -run '^TestC10ConcurrentRead$')
```
