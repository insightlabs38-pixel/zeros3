# Exact tensor sharing and range replay

The B3 oracle independently replayed the pinned Pythia F16 checkpoint and three format variants in both ingest orders. It also replayed the independently published SmolLM2 safetensors, GGUF and ONNX files in both orders. The source hashes, byte accounting and exactness checks are in the adjacent semantic permutations report.

The production ONNX layout experiment reused existing model-plane recipes, and every pinned file was read back exactly after GC and repack. It used 56,648,100 file bytes for Pythia versus 56,541,939 for generic delta and 755,315,167 for SmolLM2 versus 745,235,150 for generic delta. Layout recognition alone missed the sharing found by B3.

The separate tensor grid prototype encoded four actual transpose pairs from each model family. Both orientations produced the same canonical file and independent per-tile digests. A 4 KiB read of the transposed view fetched 8–130 KiB of tiles. Whole row-band readback verified the independently pinned output SHA on all 16 views. Observed transpose throughput was 45.9–332.6 MiB/s on the Pythia pairs and 37.1–152.7 MiB/s on the SmolLM2 pairs. Each tile digest table added 1.56% to its payload. The measurements use local file reads under shared load. They do not include store-level manifests, admission costs or deployment checks.

The direct safetensors-to-ONNX SmolLM2 screen found no exact pairs in the selected size band. The published GGUF-to-ONNX pairing did. The tensor grid has no production format or integration, and the ONNX candidate has not passed the production admission threshold.
