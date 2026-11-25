# Reproduce C1

Use Debian 13 x86-64, Python 3.12.14, GCC 14.2.0, SQLite 3.53.1, zlib
1.3.2, Go 1.27.0 and an isolated scratch directory. The pinned package list
contains the benchmark clients. The S3 servers use disposable local
credentials on loopback. Reserve at least 4 GiB for the full toolset and
build one large competitor at a time. Run each benchmark separately.

From the C1 directory with a new absolute scratch directory:

```bash
W=/path/to/owned/c1-work
mkdir -p "$W"
python3 -m venv "$W/venv"
"$W/venv/bin/pip" install -r requirements.txt
P="$W/venv/bin/python"
repo=$(git rev-parse --show-toplevel)
git -C "$repo" worktree add --detach "$W/frozen" dcefbcb5c2539c74a0d33bfc0d6e4a380f671743
(cd "$W/frozen" && GOCACHE="$W/go-build" go build -o "$W/zeros3" .)
gcc -Wl,--build-id=none -Os -s -o "$W/fixture.elf" native.c
"$P" corpus.py "$W/corpus" "$W/corpus.json" --native "$W/fixture.elf"
cmp "$W/corpus.json" results/corpus.json
cp results/external-corpus.json "$W/external-corpus.json"
"$P" external_corpus.py "$W/corpus" "$W/external-corpus.json"
./install.sh "$W" rustfs weed garage restic borg kopia
(cd "$W" && awk '$2 ~ /^bin\/(rustfs|weed|garage|restic|borg|kopia)$/ {print}' "$OLDPWD/results/binaries.sha256" | sha256sum -c -)
```

Result files below require fresh destinations. These commands use the
same profiles and endpoint configurations as the published primary trials.

```bash
"$P" local_s3.py "$W" "$W/s3-requests.jsonl" --lane requests --systems zeros3 rustfs seaweed garage
"$P" backup.py "$W" "$W/backup-combined.jsonl"
"$P" local_s3.py "$W" "$W/s3-history-combined.jsonl" --lane histories \
  --systems zeros3 rustfs seaweed garage --families combined
```

The runtime scripts provision and reclaim only their owned local run
directories. The four S3 request clients use a single process with a thread
pool and share the VM CPU quota with the server. Request timing includes
client signing and checksum work. Payloads are cached in the client's RAM.
An operation's time excludes object fixture generation.

For hosted measurements, run only against the service account authorized
to create the two public `storage_test` destinations. The driver prompts
securely or accepts an already injected `HF_TOKEN`. An existing resource at
that exact destination stops its lane. Run on an otherwise idle network
interface. Replace the interface name with the interface actually carrying
Hugging Face traffic. Omit the flag when unavailable and treat traffic as
unmeasured. Hosted runs leave the two authorized public resources in place.

```bash
"$P" xet.py "$W" "$W/hosted" "$W/xet-hosted.jsonl" --interface eth1
```

For OpenXet, install Rust 1.97 or later in isolated scratch, point
`C1_CARGO` at Cargo, and use the pinned source revision. MinIO has a
separate source-build route after the archived binary returned HTTP 410.

```bash
./install.sh "$W" openxet
"$P" local_xet.py "$W" "$W/openxet-run" "$W/xet-local.jsonl"
./install.sh "$W" minio
"$P" local_s3.py "$W" "$W/minio-requests.jsonl" --lane requests --systems minio
"$P" local_s3.py "$W" "$W/minio-history.jsonl" --lane histories --systems minio --families combined
```

Use `summarize.py` to reproduce the generated tables from the committed
results and `plot.py` with Matplotlib 3.10.8 for the tradeoff chart.

Run the family and protocol diagnostics separately from the primary
repeat trials. The deterministic Parquet file hashes matched a second
generation. When reproducing SST, write a new SHA-256 manifest for the
producer's run-specific bytes.

```bash
"$W/venv/bin/pip" install -r requirements-codecs.txt
"$P" codec_corpus.py "$W/corpus" "$W/codec-corpus.json"
"$P" corpus_stats.py "$W/corpus" "$W/corpus.json" "$W/external-corpus.json" "$W/codec-corpus.json"
"$P" local_s3.py "$W" "$W/s3-history-families.jsonl" --lane histories --systems zeros3 \
  --families package oci tensor shift entropy coreutils lodash --profiles plain archive archive-delta --repetitions 1
"$P" local_s3.py "$W" "$W/s3-codecs.jsonl" --lane histories --systems zeros3 \
  --families parquet sst --profiles plain archive archive-delta --repetitions 1
"$P" backup.py "$W" "$W/backup-kopia-zstd.jsonl" --systems kopia --kopia-compression zstd-fastest
"$P" local_xet.py "$W" "$W/openxet-no-chunk-cache" "$W/xet-local-no-chunk-cache.jsonl" --chunk-cache-mib 0
"$P" xet_comparison.py "$W" "$W/zeros3-xet-trace.jsonl"
"$P" socket_probe.py "$W" "$W/socket-probe-retry.jsonl"
"$P" restart_check.py "$W" "$W/restart-check.jsonl"
C1_WEED_VOLUMES=8 "$P" local_s3.py "$W" "$W/seaweed-8-volumes.jsonl" --lane requests --systems seaweed
```
