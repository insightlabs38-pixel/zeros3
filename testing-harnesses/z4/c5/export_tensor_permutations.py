#!/usr/bin/env python3
"""Build controlled GGUF and ONNX exports of one pinned F16 checkpoint."""
import argparse
import hashlib
import importlib.metadata
import json
from pathlib import Path

import gguf
import numpy as np
import onnx
from onnx import helper, numpy_helper
from safetensors.numpy import load_file


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def export_gguf(path, tensors):
    writer = gguf.GGUFWriter(path, "gptneox")
    try:
        for name in reversed(sorted(tensors)):
            writer.add_tensor(name, np.ascontiguousarray(tensors[name]))
        writer.write_header_to_file()
        writer.write_kv_data_to_file()
        writer.write_tensors_to_file()
    finally:
        writer.close()


def export_onnx(path, tensors, transpose):
    initializers = []
    for name in sorted(tensors):
        array = tensors[name]
        if transpose and array.ndim == 2:
            array = np.ascontiguousarray(array.T)
        initializers.append(numpy_helper.from_array(array, name.replace(".", "_")))
    graph = helper.make_graph([], "checkpoint_tensors", [], [], initializer=initializers)
    model = helper.make_model(graph)
    model.ir_version = 8
    onnx.save(model, path)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--sha256", required=True)
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()
    actual = digest(args.input)
    if actual != args.sha256:
        parser.error(f"checkpoint SHA-256 is {actual}, expected {args.sha256}")
    tensors = load_file(str(args.input))
    if not tensors or any(a.dtype != np.dtype("<f2") for a in tensors.values()):
        parser.error("the controlled producer expects a checkpoint with only F16 tensors")
    args.out.mkdir()
    paths = [args.out / name for name in ("reverse.gguf", "forward.onnx", "transpose.onnx")]
    export_gguf(paths[0], tensors)
    export_onnx(paths[1], tensors, False)
    export_onnx(paths[2], tensors, True)
    manifest = {
        "source": args.input.name,
        "source_sha256": actual,
        "tensors": len(tensors),
        "producer_versions": {
            name: importlib.metadata.version(name) for name in ("numpy", "safetensors", "gguf", "onnx")
        },
        "exports": [
            {"file": p.name, "size": p.stat().st_size, "sha256": digest(p)} for p in paths
        ],
    }
    with (args.out / "manifest.json").open("x") as stream:
        json.dump(manifest, stream, indent=2, sort_keys=True)
        stream.write("\n")


if __name__ == "__main__":
    main()
