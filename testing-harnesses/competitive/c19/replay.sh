#!/usr/bin/env bash
set -euo pipefail
harness=$(cd "$(dirname "$0")" && pwd)
scratch=$(realpath -m "${1:?pass C19 scratch}")
step=${2:?pass general, retention, qos, transport, codec, backup, openxet, compatibility, acceptance, or merged}
shared=${C19_SHARED:-/workspace/work}
tools=${C19_COMPETITORS:-$shared/c1}
compiler=${C19_GO:-go}
python=${C19_PYTHON:-python3}
downloads=${C19_DOWNLOADS:-$shared/c2/downloads}
corpus=${C19_C14_CORPUS:-$shared/c14/final-corpus}
inputs=${C19_INPUTS:-$scratch/inputs}
baseline=${C19_C9:-$scratch/zeros3-c9}
integrated=${C19_INTEGRATED:-$scratch/zeros3-integrated}
export GOMAXPROCS=2
mkdir -p "$scratch"

case "$step" in
  general)
    "$python" "$harness/general_s3.py" --c9 "$baseline" --integrated "$integrated" \
      --competitors "$tools" --work "$scratch/general" --results "$scratch/general.jsonl"
    ;;
  retention)
    shift 2
    "$python" "$harness/retention.py" --c9 "$baseline" --integrated "$integrated" --inputs "$inputs" \
      --downloads "$downloads" --corpus "$corpus" --manifest "${C19_C14_MANIFEST:-$shared/c14/final-fixtures.json}" \
      --fs-tools "${C19_FS_TOOLS:-$shared/c14/tools}" --work "$scratch/retention" --results "$scratch/retention.jsonl" "$@"
    ;;
  qos)
    qos=(--baseline "$baseline" --candidate "$integrated" --work "$scratch/qos" --downloads "$downloads")
    "$python" "$harness/../c11/qos.py" "${qos[@]}" --prepare --results "$scratch/unused.jsonl"
    "$python" "$harness/../c11/qos.py" "${qos[@]}" --events mixed --widths 4 --operations 512 \
      --lanes idle incumbent adaptive --repeats 2 --deep-verify --check-leases --results "$scratch/qos.jsonl"
    ;;
  transport)
    "$python" "$harness/transport.py" --binary "$integrated" --downloads "$downloads" \
      --c1 "${C19_C1_CORPUS:-$shared/c1/corpus}" --work "$scratch/transport" --results "$scratch/transport.jsonl"
    ;;
  backup)
    "$python" "$harness/snapshot_controls.py" --binaries "$tools/bin" --corpus "$corpus" \
      --work "$scratch/backup" --results "$scratch/backup.jsonl"
    ;;
  openxet)
    "$python" "$harness/../c16/openxet.py" --binary "$tools/bin/openxet" --downloads "$downloads" \
      --c1 "${C19_C1_CORPUS:-$shared/c1/corpus}" --c14 "$corpus" --work "$scratch/openxet" --results "$scratch/openxet.jsonl"
    ;;
  codec)
    codec="$scratch/codec-inputs"
    mkdir -p "$codec"/{urllib3,chinook}/{0,1,2}
    ln -s "$inputs/urllib3-2.2.2.tar.gz" "$codec/urllib3/0/"
    ln -s "$inputs/urllib3-2.2.3.tar.gz" "$codec/urllib3/1/"
    ln -s "$inputs/chinook-1.4.4.sqlite" "$codec/chinook/0/"
    ln -s "$inputs/chinook-1.4.5.sqlite" "$codec/chinook/1/"
    ln -s "$scratch/retention/chinook-edited.sqlite" "$codec/chinook/2/"
    "$python" "$harness/../c12/compare.py" --lab "${C19_DELTA_LAB:-$shared/c12/lab/c12.test}" \
      --tools "${C19_DELTA_TOOLS:-$shared/c12/tools/usr/bin}" --corpus "$codec" --work "$scratch/codec" \
      --results "$scratch/codec.jsonl" --families urllib3 chinook --methods xd xdelta3 zstd --pairs 0 1
    ;;
  compatibility)
    "$python" "$harness/compatibility.py" --frozen "$integrated" --old "$baseline" \
      --fixture "${C19_REPAIR_FIXTURE:-$shared/c18/complete-repair/0-targeted/store}" \
      --copy "$scratch/compatibility" --results "$scratch/compatibility.jsonl"
    ;;
  acceptance)
    C19_GO="$compiler" "$harness/acceptance.sh" "$scratch/acceptance"
    ;;
  merged)
    merged=$(realpath "${C19_MERGED_WORKTREE:?set merged worktree}")
    "$compiler" -C "$merged" build -trimpath -o "$scratch/zeros3-merged" .
    "$python" "$harness/transport.py" --binary "$scratch/zeros3-merged" --downloads "$downloads" \
      --c1 "${C19_C1_CORPUS:-$shared/c1/corpus}" --work "$scratch/transport-merged" \
      --results "$scratch/transport-merged.jsonl" --integration-smoke
    cd "$merged"
    "$compiler" test . -count=1 -timeout=2m -v \
      -run '^(TestC1(6|7|8)|TestAuthorityClosureSync.*|TestBulkReplicate_ThroughStandardReverseProxy|TestFB02_(ReplicateEquivalenceCases|MultipartOriginEquivalence|CreateOnlyRefusesETagEqualContentDifferent|NamespaceRerunWritesNothing|CommitIfUnchanged|ConcurrentIfUnchangedCommits|IfUnchangedCrashCuts|SnapshotDescribeAndRestoreEquivalence|AuthorityImportRetryWritesNoManifest))' \
      > "$scratch/merged-tests.txt" 2>&1
    "$compiler" test -race . -count=1 -timeout=2m -v \
      -run '^(TestC16NamedBaseReplication|TestFB02_ConcurrentIfUnchangedCommits)$' \
      > "$scratch/merged-race.txt" 2>&1
    ;;
  *)
    exit 2
    ;;
esac
