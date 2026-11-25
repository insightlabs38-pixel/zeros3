#!/usr/bin/env bash
# Replay final local comparison lanes using fresh result paths.
set -euo pipefail
work=$(realpath "${1:?pass the final work directory}")
c1=$(realpath "${2:?pass existing C1 work with pinned inputs and OpenXet}")
corpus=$(realpath "${3:?pass the frozen C5 corpus}")
releases=$(realpath "${4:?pass the frozen C2 release downloads}")
py=${C9_PYTHON:-python3}
cd "$(dirname "$0")"
"$py" workflows.py --binary "$work/zeros3" --restic "$work/bin/restic" --corpus "$corpus" \
  --scratch "$work" --results "$work/workflows.jsonl"
"$py" ../c1/local_s3.py "$work" "$work/requests.jsonl" --lane requests --systems zeros3 minio rustfs --repetitions 2
"$py" ../c8/stream.py --baseline "$work/zeros3" --candidate "$work/zeros3" --corpus "$c1/corpus" \
  --downloads "$releases" --scratch "$work" --results "$work/transfer.jsonl" \
  --final-only --cases typescript --ordinary --rate 768000 --repeats 2
"$py" xet_repeat.py "$c1" "$work/xet" "$work/xet.jsonl"
