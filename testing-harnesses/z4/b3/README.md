# B3 reproduction (research only)
Go 1.27, Python 3.13 (numpy, torch cpu, safetensors, gguf, onnx, pyarrow, zstandard), `git`, `gzip`. No production code is touched;
Python/pyarrow/torch are producers and independent oracles only, never needed to replay.

```sh
go -C testing-harnesses/z3 test ./semcas                      # unit, property, adversarial tests
go -C testing-harnesses/z3 build -o /tmp/z4b3 ./cmd/z4b3
# pinned inputs (Hugging Face): see hf_pins.txt; then
python testing-harnesses/z4/b3/make_pythia_exports.py HF/pythia HF/pythia_ctrl     # controlled exports
testing-harnesses/z4/b3/run_tensor_evidence.sh /tmp/z4b3 HF WORK                   # SmolLM2 + Pythia lanes
testing-harnesses/z4/b3/run_other_evidence.sh /tmp/z4b3 "$PWD" WORK2               # Git/tar/OCI + columnar
python testing-harnesses/z4/b3/summarize.py WORK WORK2 > evidence.json            # compact evidence
```
`z4b3 ingest|files` build a store (`blobs/`, `comps/`, `recipes/`); `z4b3 replay` reconstructs every original from the serialized
store alone (fresh process); `verify_replay.py` hashes every output and original independently of the Go code.
`optimize` is the offline group-aware re-rooting pass; `ranges` measures 4 KiB/64 KiB/1 MiB reads; `b1-sample` prices the B1 delta on tensor windows.
