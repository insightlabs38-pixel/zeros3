"""Price exact posting directories over fixed-size and hot-key inputs."""
import argparse
from collections import Counter
import json
import math
from pathlib import Path
import random


def layouts(features):
    base = 28
    counts = Counter(f >> 48 for f in features)
    def radix(bits):
        suffix = math.ceil((64-bits)/8)
        return base + 4*(2**bits+1) + (suffix+4)*len(features)
    return {"radix16_fixed": radix(16), "global_sorted": radix(0),
            "sparse_16_pairs": base+6*(len(counts)+1)+10*len(features),
            "sparse_16_bitmap": base+8192+4*(len(counts)+1)+10*len(features),
            "adaptive_8_12_16": min(radix(0), radix(8), radix(12), radix(16)),
            "unique_48_prefixes": len(counts), "hot_bucket": max(counts.values())}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--results", type=Path, required=True)
    args = parser.parse_args()
    rng = random.Random(1515)
    with args.results.open("x") as out:
        for count in (16, 1024, 16384, 65536, 262144):
            for workload in ("uniform", "20-percent-hot"):
                n_hot = count//5 if workload == "20-percent-hot" else 0
                features = [rng.getrandbits(64) for _ in range(count-n_hot)]
                features.extend([0xc9b78426d9a531fd]*n_hot)
                print(json.dumps({"count": count, "distribution": workload, **layouts(features)}), file=out)


if __name__ == "__main__":
    main()
