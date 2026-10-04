#!/usr/bin/env bash
# validate.sh -- aggregate ZeroS3 validation gates.
#
#   scripts/validate.sh normal   format/vet/module checks, full root tests,
#                                static map checks, s3+sync harness groups,
#                                reproducible build
#   scripts/validate.sh heavy    race tests, pack/compression crash matrix,
#                                multi-GiB repack, compaction and pack
#                                compression harnesses, 5M-record locator scale,
#                                bulk-transfer request/RTT benchmark,
#                                history prune lifecycle
#   scripts/validate.sh index    locator semantic/differential tests only
#   scripts/validate.sh bulk     bulk framing/protocol tests plus the sync,
#                                replicate, restore and repair suites
#   scripts/validate.sh history  history prune planner/journal/crash/reclamation tests
#   scripts/validate.sh history-life  black-box prune lifecycle harness (gc + repack)
#   scripts/validate.sh tier     hot/warm/cold tier unit, crash and CLI tests (fast)
#   scripts/validate.sh tier-life  black-box tier lifecycle harness (compact -tier,
#                                tier move, prune + gc/repack, v1-v5 format compat)
#   scripts/validate.sh tier-policy  content-aware tier policy: rule resolution, persistence,
#                                hottest-reference target computation (seconds)
#   scripts/validate.sh tier-rebalance  tier rebalance planner/executor/crash tests plus the
#                                black-box rebalance scenario (browser + checkpoint)
#   scripts/validate.sh cas-batch  grouped loose-CAS publication: batch/barrier/crash/concurrency tests,
#                                real-process kill, bulk-upload batching (under a minute)
#   scripts/validate.sh bundle  portable snapshot bundles: format/parser matrix, export/import,
#                                idempotence, collisions, crash matrix, grouped CAS publication (seconds)
#   scripts/validate.sh bundle-life  bundle scenarios: browser/checkpoint round trips, source deletion + GC,
#                                real-process kill during import
#   scripts/validate.sh bundle-scale  chunk-planner scale sanity (1M synthetic descriptors)
#   scripts/validate.sh vectors  golden client vectors (SigV4, presign, wire shapes) plus the
#                                fast GetBucketLocation/DeleteObjects/probe tests (seconds)
#   scripts/validate.sh client   Core Client Profile v1 (AWS SDK + minio-go [+ AWS CLI if
#                                installed]) against a managed server, plus physical-state invariance
#   scripts/validate.sh apps     browser-site and artifact/checkpoint application scenarios
#   scripts/validate.sh all      both
#   scripts/validate.sh STAGE... run named stages (see `list`)
#
# Every stage logs to $VALIDATE_LOG_DIR (default /tmp/zeros3-validate) and a
# failing stage prints the tail of its log. All requested stages run; the
# exit status is nonzero if any failed. A stage that passed for the exact
# current sources is skipped on the next run (VALIDATE_FORCE=1 reruns it).
# Harness sizes: REPACK_SIZE_MIB (default 1024 per profile), COMPACT_SIZE_MIB,
# COMPRESS_CLASS_MIB (per data family) and COMPRESS_SIZE_MIB (lifecycle store),
# PACK_SIZE_MIB, LOCATOR_SCALE (record counts for index-scale, default 5000000),
# HISTORY_SEG_MIB (history-life object half size, default 4), HISTORY_BASELINE_BIN
# (a build predating history pruning; default: built from the Z2-07 merge),
# BULK_SIZE_MIB (bulk-bench payload, default 256), BULK_BASELINE_BIN (a build
# without bulk transport to act as the v1 client; default: built from the
# Z2-06 commit), TIER_SEG_MIB (tier-life object segment size, default 4),
# TIER_BASELINE_BIN (the Z2-08 build, store formats 1-4; default: built from
# its merge commit 33478ce), CAS_SIZE_MIB (cas-bench PutObject size, default 256),
# CAS_MULTIPART_MIB (cas-bench multipart size, default 256), CAS_BASELINE_BIN (a build
# predating grouped CAS publication; default: built from the Z2-10 merge a2bf462).
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
stage_bulk() {
	cd "$root" && go test -count=1 -run 'TestBulk|TestSync_|TestReplicate_|TestRepair_|TestSnapshotRestore' .
}
stage_history() {
	cd "$root" && go test -count=1 -run 'TestPrune_|TestJournal_|TestVersions_|TestRestore_|TestCrash_JournalReplay|TestPack_StoreFormatVersionGate' .
}
stage_history-life() {
	build_bin || return 1
	base="${HISTORY_BASELINE_BIN:-}"
	if [ -z "$base" ]; then
		base="$logs/zeros3-z2-07-baseline"
		tmp=$(mktemp -d) &&
			(cd "$root" && git archive 24ec2a6 zeros3.go go.mod | tar -x -C "$tmp") &&
			(cd "$tmp" && CGO_ENABLED=0 go build -o "$base" zeros3.go) || return 1
		rm -rf "$tmp"
	fi
	cd "$root/testing-harnesses" &&
		ZEROS3_BIN="$bin" go run ./harness/z2_history_prune -seg-mib "${HISTORY_SEG_MIB:-4}" -baseline-bin "$base"
}
stage_bulk-bench() {
	build_bin || return 1
	base="${BULK_BASELINE_BIN:-}"
	if [ -z "$base" ]; then
		base="$logs/zeros3-v1-baseline"
		tmp=$(mktemp -d) &&
			(cd "$root" && git archive 0b3595c zeros3.go go.mod | tar -x -C "$tmp") &&
			(cd "$tmp" && CGO_ENABLED=0 go build -o "$base" zeros3.go) || return 1
		rm -rf "$tmp"
	fi
	cd "$root/testing-harnesses" &&
		go run ./harness/z2_bulk_transfer -bin "$bin" -baseline-bin "$base" -size-mib "${BULK_SIZE_MIB:-256}" \
			-delays-ms 0,5,10 -v1-workers 8 -v2-workers 8 -matrix-delay-ms 10
}
stage_cas-batch() { cd "$root" && go test -count=1 -run 'TestCASBatch_|TestBulkUpload_|TestRepair_' .; }
stage_cas-bench() {
	build_bin || return 1
	base="${CAS_BASELINE_BIN:-}"
	if [ -z "$base" ]; then
		base="$logs/zeros3-z2-10-baseline"
		tmp=$(mktemp -d) &&
			(cd "$root" && git archive a2bf462 zeros3.go go.mod | tar -x -C "$tmp") &&
			(cd "$tmp" && CGO_ENABLED=0 go build -o "$base" zeros3.go) || return 1
		rm -rf "$tmp"
	fi
	cd "$root/testing-harnesses" &&
		go run ./harness/z2_cas_batch -bin "$bin" -baseline-bin "$base" -size-mib "${CAS_SIZE_MIB:-256}" -multipart-mib "${CAS_MULTIPART_MIB:-256}" &&
		go run ./harness/z2_bulk_transfer -bin "$bin" -size-mib "${BULK_SIZE_MIB:-256}" -delays-ms 0,10 -v2-workers 8
}
stage_bundle() { cd "$root" && go test -count=1 -run 'TestBundle_|TestCASShardDirs_' .; }
stage_bundle-life() { cd "$root" && go test -count=1 -run 'TestBundleLife_' .; }
stage_bundle-scale() { cd "$root" && go test -count=1 -run 'TestBundleScale_' -v .; }
stage_tier() { cd "$root" && go test -count=1 -run 'TestTier_|TestLocator_' .; }
stage_tier-life() {
	build_bin || return 1
	base="${TIER_BASELINE_BIN:-}"
	if [ -z "$base" ]; then
		base="$logs/zeros3-z2-08-baseline"
		tmp=$(mktemp -d) &&
			(cd "$root" && git archive 33478ce zeros3.go go.mod | tar -x -C "$tmp") &&
			(cd "$tmp" && CGO_ENABLED=0 go build -o "$base" zeros3.go) || return 1
		rm -rf "$tmp"
	fi
	cd "$root/testing-harnesses" &&
		ZEROS3_BIN="$bin" go run ./harness/z2_storage_tiers -seg-mib "${TIER_SEG_MIB:-4}" -baseline-bin "$base"
}
stage_tier-policy() { cd "$root" && go test -count=1 -run 'TestTierPolicy_' .; }
stage_tier-rebalance() {
	cd "$root" && go test -count=1 -run 'TestTierRebalance_' . && build_bin &&
		cd "$root/testing-harnesses" && ZEROS3_BIN="$bin" go run ./harness/z2_consumer -scenario rebalance
}
stage_vectors() {
	cd "$root" && go test -count=1 -run 'TestVectors_|TestGetBucketLocation|TestDeleteObjects_|TestProbe|TestListObjectsV2_' . &&
		{ ! command -v python3 >/dev/null || python3 testing-harnesses/vectors/gen.py --check; }
}
stage_client() { build_bin && cd "$root/testing-harnesses" && go run ./runner -group client -bin "$bin"; }
stage_apps() { build_bin && cd "$root/testing-harnesses" && go run ./runner -group apps -bin "$bin"; }
stage_repro() { cd "$root" && sh scripts/reproducible_build.sh; }

stage_race()    { cd "$root" && go test -race -count=1 ./...; }
stage_crash()   { cd "$root" && go test -race -count=1 -run 'TestRepack_|TestPack_|TestPackCompress|TestTier_|TestTierRebalance_' .; }
stage_index() { cd "$root" && go test -count=1 -race -run 'TestLocator_' .; }
stage_index-scale() { cd "$root" && ZEROS3_LOCATOR_SCALE="${LOCATOR_SCALE:-5000000}" go test -count=1 -run 'TestLocatorScale' -v .; }
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
heavy="race crash repack compact compression index-scale bulk-bench cas-bench history-life tier-life"

case "${1:-normal}" in
list) echo "normal: $normal"; echo "heavy: $heavy"; echo "focused: index bulk cas-batch bundle bundle-life bundle-scale history history-life tier tier-policy tier-rebalance tier-life vectors client apps"; exit 0 ;;
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
