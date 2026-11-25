#!/usr/bin/env bash
# Reuse the established request, history, backup, and transfer fixtures.
set -euo pipefail
work=$(realpath "${1:?pass the seeded C5 work directory}")
baseline=$(realpath "${2:?pass the frozen C2 binary}")
bridge=$(realpath "${3:?pass the frozen C1 corpus}")
releases=$(realpath "${4:?pass the frozen C2 release downloads}")
py=${C5_PYTHON:-python3}
cd "$(dirname "$0")"
families=(coreutils parquet sst entropy typescript httpx mobilenet)
"$py" inputs.py "$work/corpus" "$bridge" "$releases" inputs.json
"$py" ../c1/local_s3.py "$work" "$work/requests.jsonl" --lane requests --systems zeros3 minio rustfs --repetitions 3
"$py" peer_history.py "$work" "$work/peers.jsonl" --families "${families[@]}"
"$py" ../c2/frontier.py "$baseline" "$work/corpus" "$work/frontier-c2" "$work/c2.jsonl" \
  --families "${families[@]}" --full --lanes cdc c1 --reads
"$py" ../c2/frontier.py "$work/zeros3" "$work/corpus" "$work/frontier-current" "$work/current.jsonl" \
  --families "${families[@]}" --full --lanes cdc c1 mixed --reads
"$py" ../c1/backup.py "$work" "$work/restic.jsonl" --systems restic --families coreutils parquet sst --repetitions 1
"$py" ../c3/transfer.py --binary "$work/zeros3" --corpus "$bridge" --downloads "$releases" \
  --scratch "$work/relay" --results "$work/transfer.jsonl" --families typescript \
  --states different-authority --lanes s3 sync replicate closure-cross --rate-bytes 768000 --repeats 2
"$py" decision.py "$work" > "$work/decision.json"
