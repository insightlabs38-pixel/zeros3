# Retained history pilot

The pilot writes each real revision at an immutable key and overwrites a
second key in its family to exercise retained history. The two stores start
from the same closed store. Both run GC and recompressing repack. The
candidate additionally runs cold-tier authority migration. The harness
measures allocated filesystem bytes, per-process CPU and peak RSS, sampled
write headroom, authenticated whole and 4 KiB reads, retention timestamps
and independently hashed response bytes. A fresh server starts each sample
with a 64 MiB chunk-cache budget. The OS page cache is uncontrolled, and
families can share cache entries across requests. The repeat workload gives
newer revisions eight range reads and older revisions one each. These are
controlled request counts, with no claim about real customer's access rates.

Use Python 3.11 or later on Linux, a curl build supporting SigV4 and a
ZeroS3 release binary. Give the pilot a new scratch directory and supply
versions in chronological order. Source paths are retained for independent
range comparison. The first run performs the complete preparation and
records a first sample. Subsequent samples preserve earlier successful and
failed results and alternate the two stores' read order.

```sh
python3 -I testing-harnesses/z4/c4/pilot.py prepare --binary "$BIN" \
  --run-dir "$RUN" \
  --history coreutils-tar "$SRC/coreutils-9.5.tar" "$SRC/coreutils-9.6.tar" "$SRC/coreutils-9.7.tar" \
  --history coreutils-gzip "$SRC/coreutils-9.5.tar.gz" "$SRC/coreutils-9.6.tar.gz" "$SRC/coreutils-9.7.tar.gz" \
  --history pythia "$MODEL/step100000.safetensors" "$MODEL/step101000.safetensors" "$MODEL/step102000.safetensors"
python3 -I testing-harnesses/z4/c4/pilot.py sample --binary "$BIN" --run-dir "$RUN"
```

## Initial screen

Two runs on the same prepared stores independently checked all 96 signed
reads per run and both deep verifies. Each store held 12 current objects
and six historical entries. Cold admitted one of nine contents. It used
179,015,680 bytes after GC and repack, or 2.54% more than the CDC store
at 174,579,712 bytes. Cold migration used 81.2 CPU seconds and 208,496 KiB
peak RSS. Its sampled peak store allocation was 550,735,872 bytes, up
from 309,293,056 bytes before migration. The whole-read throughput ratios
were 0.87 and 1.15 on first-process reads, and 0.53 and 0.71 on repeats.
Runs shared a two-CPU quota with qualification, so the throughput ratios
describe those samples. No blended savings claim passed this screen.
Extended integrity remains **PENDING 30-DAY RUN**.

The corpus digests, admission reasons, maintenance metrics, per-request
times, retained-version timestamps and deep verification outcomes are in
[initial-pilot.json](results/initial-pilot.json).
