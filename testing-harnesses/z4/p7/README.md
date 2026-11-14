# Z4-P7 evidence and reproduction

Report: `testing-harnesses/results/Z4_P7_MODEL_HARDCODEC_PERFORMANCE.md`. Everything here is research
harness code (`testing-harnesses/z3`, module `zeros3-z3`); production `zeros3.go` / `go.mod` are unchanged.

## Code map (all under `testing-harnesses/z3`)

| Package | Content |
|---|---|
| `qcore` | byte-exact ggml quantizers (Q4_0 Q4_1 Q5_0 Q5_1 Q8_0 Q2_K Q3_K Q4_K Q5_K Q6_K IQ4_NL IQ4_XS), reference and imatrix paths, `Strict` and `GCCFMA` arithmetic profiles, imatrix parser |
| `numcore` | ZN7 windowed model container: bounded-memory encoder/decoder, range reads, predictors, quant derivations with block patches, cost model |
| `mdelta2` | B5 engine, now a thin shim over `numcore`/`qcore` (B5 tests unchanged) |
| `brotliz3` | exact Go port of the google/brotli one-shot encoder (quality 5-9, lgwin 17-24) + bounded decoder; `brdict` embeds the RFC 7932 dictionary |
| `lz4z3` | + pierrec/lz4 predictor, LZ4HC optimal parser (levels 10-12), linked-block frame predictors, memoizing `Analyzer` |
| `sstz3`, `parquetz3`, `sqfsz3` | consumers: memoized LZ4 analysis, Brotli page codec, `DecodeParallel` |
| `cmd/z4p7` | `quantconf`, `family`, `replay`, `ranges`, `pqpages`, `brenc`, `brdec`, `gen-big` |
| `oracle/quant`, `oracle/brotli`, `oracle/lz4` | reference producers used to generate golden vectors |

## Pins

- llama.cpp tag `b3991` (`ggml/src/ggml-quants.c`), GCC 13.3, x86-64: `oracle/quant/build.sh LLAMA_SRC OUT`.
- google/brotli as bundled in pyarrow 26.0.1 (one-shot `BrotliEncoderCompress`) and python-brotli 1.2.1.
- liblz4 1.9.4 via python-lz4 4.4.5; pierrec/lz4 v4.1.22 (`oracle/lz4/pierrecgen`).
- Models: `z4/b5/pins.json` (float families), `z4/p7/quant_pins.json` (SmolLM2-135M-Instruct GGUF sets of bartowski and QuantFactory);
  Qwen2.5 GGUF checks: QuantFactory/Qwen2.5-0.5B-Instruct-GGUF@be5d6f8 and bartowski/Qwen2.5-0.5B-Instruct-GGUF@41ba88d (Q2_K files, imatrix),
  QuantFactory/Qwen2.5-1.5B-Instruct-GGUF@a5e8aad, bartowski/Qwen2.5-1.5B-Instruct-GGUF@9eadc66, Qwen/Qwen2.5-1.5B-Instruct@989aa79.

## Reproduce

```sh
cd testing-harnesses/z3 && go build -o $B/z4p7 ./cmd/z4p7
python3 ../z4/b5/fetch.py $D                      # float families (B5 pins)
python3 ../z4/p7/fetch_quant.py $D/quant          # quantized sets + imatrix

# A. producer conformance (per type: blocks byte-identical to the producer)
$B/z4p7 quantconf -src $D/quant/bw/f16.gguf -imatrix $D/quant/bw/imatrix.dat $D/quant/bw/Q*.gguf $D/quant/bw/IQ*.gguf
$B/z4p7 quantconf -src $D/quant/bw/f16.gguf $D/quant/qf/*.gguf
$B/z4p7 quantconf -src $D/qwen05/instruct.safetensors [-imatrix F] FILE.gguf     # Safetensors source, also tries the F16 step

# B. numeric engine vs B5 (interleaved, fresh processes): scripts/bench_model.sh, scripts/bench_model2.sh
$B/z4p7 family -store S -policy pool:8 [-structured] [-exhaustive] FILES...     # encode a family into a ZN7 store
$B/z4p7 replay -store S -workers 4 [-out DIR]                                   # rebuild + verify every state
$B/z4p7 ranges -store S -orig FILE                                              # range amplification

# C/D. hard codecs
go test ./brotliz3 ./lz4z3 ./sstz3 ./parquetz3 ./sqfsz3
$B/z4b6 lz4frames FILE.lz4...                    # frame analysis + exact replay
$B/z3parquet perf -phase encode|decode [-workers N] -file F.parquet -store DIR

# qualification (format, vet, tests of all dependents, race, fuzz, extraction + rehearsal, production unchanged)
bash testing-harnesses/z4/p7/scripts/qualify.sh OUT        # from the repository root
```

Golden vectors are regenerated with `QCORE_ORACLE=... QCORE_WRITE_GOLDEN=1 go test ./qcore`,
`oracle/brotli/refenc.py`, `oracle/lz4/hcref.py`, `oracle/lz4/linkref.py`, `oracle/lz4/pierrecgen`.

## Evidence (`results/`)

- `quantconf*.jsonl|txt`: producer-conformance matrix rows.
- `model/`: interleaved B5 vs P7 runs (`*.jsonl`), manifests, range reads; `model/batch2/`: worker scaling,
  sampled vs exhaustive planner, structured controls, Qwen, >1 GiB pair; `model/quant/`: quantized collection.
- `hard/`: SST / Parquet timings, fresh-process exactness proofs, linked LZ4 frames, parallel Brotli replay.
- `extraction/`, `qualify/`: mechanical extraction, rehearsal and qualification logs.
