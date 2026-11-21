#!/usr/bin/env python3
"""Extract independently checked, real rank-two tensor pairs for the Go replay experiment."""
import argparse
import hashlib
import json
from pathlib import Path

import gguf
import onnx
from onnx import numpy_helper
from safetensors.numpy import load_file


def stamp(raw):
    return hashlib.sha256(raw).hexdigest()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("basis", type=Path, help="safetensors or GGUF")
    p.add_argument("onnx", type=Path)
    p.add_argument("output", type=Path)
    p.add_argument("--max-pairs", type=int, default=4)
    a = p.parse_args()
    a.output.mkdir()
    if a.basis.suffix == ".gguf":
        reader = gguf.GGUFReader(str(a.basis))
        arrays = {tensor.name: tensor.data for tensor in reader.tensors}
    else:
        arrays = load_file(str(a.basis))
    known = {}
    for name, matrix in sorted(arrays.items()):
        if matrix.ndim != 2 or matrix.nbytes < 64 << 10 or matrix.nbytes > 16 << 20:
            continue
        raw = matrix.tobytes()
        transposed = matrix.T.tobytes()
        if raw == transposed:
            continue
        known.setdefault((matrix.T.shape, matrix.dtype.str, stamp(transposed)), (name, matrix))
    model = onnx.load(str(a.onnx), load_external_data=False)
    pairs = []
    for tensor in model.graph.initializer:
        if tensor.external_data or len(tensor.dims) != 2 or not tensor.raw_data:
            continue
        matrix = numpy_helper.to_array(tensor)
        transposed = stamp(tensor.raw_data)
        key = (matrix.shape, matrix.dtype.str, transposed)
        if key not in known:
            continue
        source, base = known[key]
        name = f"pair-{len(pairs)}"
        base_bytes = base.tobytes()
        (a.output / (name + ".base")).write_bytes(base_bytes)
        (a.output / (name + ".transpose")).write_bytes(tensor.raw_data)
        pairs.append({"Name": name, "BaseSHA256": stamp(base_bytes), "TransposeSHA256": transposed,
                      "Rows": base.shape[0], "Cols": base.shape[1], "Width": base.dtype.itemsize,
                      "SourceTensor": source, "TargetTensor": tensor.name})
        if len(pairs) == a.max_pairs:
            break
    if not pairs:
        raise RuntimeError("zero exact transpose pairs in the specified fixture band")
    (a.output / "manifest.json").write_text(json.dumps(pairs, indent=2) + "\n")


if __name__ == "__main__":
    main()
