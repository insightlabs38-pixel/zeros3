#!/usr/bin/env bash
# validate.sh -- aggregate ZeroS3 validation gates.
#
#   scripts/validate.sh normal   format/vet/module checks, full root tests,
#                                static map checks, s3+sync harness groups,
#                                reproducible build
#   scripts/validate.sh heavy    race tests, pack/compression crash matrix,
#                                multi-GiB repack, compaction and pack
#                                compression harnesses
#   scripts/validate.sh all      both
#   scripts/validate.sh STAGE... run named stages (see `list`)
#
# Every stage logs to $VALIDATE_LOG_DIR (default /tmp/zeros3-validate) and a
# failing stage prints the tail of its log. All requested stages run; the
# exit status is nonzero if any failed. A stage that passed for the exact
# current sources is skipped on the next run (VALIDATE_FORCE=1 reruns it).
# Harness sizes: REPACK_SIZE_MIB (default 1024 per profile), COMPACT_SIZE_MIB,
# COMPRESS_CLASS_MIB (per data family) and COMPRESS_SIZE_MIB (lifecycle store),
# PACK_SIZE_MIB.
set -u

# Ambient AWS_* settings would override the harnesses' fixed credentials.
for v in $(env | sed -n 's/^\(AWS_[A-Za-z0-9_]*\)=.*/\1/p'); do unset "$v"; done

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
logs=${VALIDATE_LOG_DIR:-/tmp/zeros3-validate}
mkdir -p "$logs"
bin="$logs/zeros3-bin"

fingerprint() {
	(cd "$root" && find . \( -name '*.go' -o -name go.mod -o -name go.sum -o -name '*.sh' \) -not -path './.git/*' -print0 |
		sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1)
}
fp=$(fingerprint)

build_bin() { (cd "$root" && CGO_ENABLED=0 go build -o "$bin" zeros3.go); }

stage_format()  { test -z "$(cd "$root" && gofmt -l . testing-harnesses)" || { (cd "$root" && gofmt -l . testing-harnesses); return 1; }; }
stage_modules() {
	cd "$root" && ! grep -q '^require\|^\s*require\s' go.mod && echo "root go.mod: no require directives" &&
		go vet ./... && (cd testing-harnesses && go vet ./...)
}
stage_test()    { cd "$root" && go test -count=1 ./...; }
stage_static() {
	build_bin && cd "$root/testing-harnesses" && go run ./runner -group static -bin "$bin"
}
stage_s3() { build_bin && cd "$root/testing-harnesses" && go run ./runner -group s3 -bin "$bin"; }
stage_sync() { build_bin && cd "$root/testing-harnesses" && go run ./runner -group sync -bin "$bin"; }
stage_repro() { cd "$root" && sh scripts/reproducible_build.sh; }

stage_race()    { cd "$root" && go test -race -count=1 ./...; }
stage_crash()   { cd "$root" && go test -race -count=1 -run 'TestRepack_|TestPack_|TestPackCompress' .; }
stage_repack() {
	build_bin && cd "$root/testing-harnesses" &&
		ZEROS3_BIN="$bin" go run ./harness/z2_repack -size-mib "${REPACK_SIZE_MIB:-1024}" -pack-size-mib "${PACK_SIZE_MIB:-32}"
}
stage_compact() {
	build_bin && cd "$root/testing-harnesses" &&
		ZEROS3_BIN="$bin" go run ./harness/z2_packed_cas -size-mib "${COMPACT_SIZE_MIB:-1024}" -pack-size-mib "${PACK_SIZE_MIB:-32}"
}

stage_compression() {
	build_bin && cd "$root/testing-harnesses" &&
		ZEROS3_BIN="$bin" go run ./harness/z2_pack_compression -class-mib "${COMPRESS_CLASS_MIB:-64}" -combined-mib "${COMPRESS_SIZE_MIB:-512}" -pack-size-mib "${PACK_SIZE_MIB:-32}"
}

normal="format modules test static s3 sync repro"
heavy="race crash repack compact compression"

case "${1:-normal}" in
list) echo "normal: $normal"; echo "heavy: $heavy"; exit 0 ;;
normal) stages=$normal ;;
heavy) stages=$heavy ;;
all) stages="$normal $heavy" ;;
*) stages="$*" ;;
esac

failed=""
summary=""
for st in $stages; do
	if ! declare -F "stage_$st" >/dev/null; then
		echo "unknown stage: $st" >&2
		exit 2
	fi
	stamp="$logs/$st.ok"
	if [ -z "${VALIDATE_FORCE:-}" ] && [ "$(cat "$stamp" 2>/dev/null)" = "$fp" ]; then
		echo "== $st: cached pass for current sources"
		summary="$summary\n  PASS(cached)  $st"
		continue
	fi
	echo "== $st: running (log $logs/$st.log)"
	start=$(date +%s)
	"stage_$st" >"$logs/$st.log" 2>&1
	rc=$?
	took=$(($(date +%s) - start))
	if [ $rc -eq 0 ]; then
		echo "$fp" >"$stamp"
		echo "== $st: PASS (${took}s)"
		summary="$summary\n  PASS  ${took}s  $st"
	else
		rm -f "$stamp"
		echo "== $st: FAIL rc=$rc (${took}s) -- last lines of $logs/$st.log:"
		tail -n 40 "$logs/$st.log"
		failed="$failed $st"
		summary="$summary\n  FAIL  ${took}s  $st"
	fi
done
printf "== summary%b\n" "$summary"
if [ -n "$failed" ]; then
	echo "== FAILED stages:$failed"
	exit 1
fi
echo "== all requested stages passed"
