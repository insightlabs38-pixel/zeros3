#!/usr/bin/env python3
"""Z3-29 PyTorch checkpoint ORACLE (research only, never a runtime dependency).

For a trusted torch.save zip checkpoint, read data.pkl with a restricted unpickler that never
builds a tensor, and print JSON: tensor name -> storage member, dtype, shape, element offset.
Run: python3 -I pt_oracle.py <checkpoint> > out.json    (torch is imported only for its version)"""
import json, pickle, sys, zipfile

DT = {"FloatStorage": ("F32", 4), "HalfStorage": ("F16", 2), "BFloat16Storage": ("BF16", 2), "DoubleStorage": ("F64", 8),
      "LongStorage": ("I64", 8), "IntStorage": ("I32", 4), "ShortStorage": ("I16", 2), "CharStorage": ("I8", 1),
      "ByteStorage": ("U8", 1), "BoolStorage": ("BOOL", 1)}

class Storage:
    def __init__(self, name): self.name = name

class Stub:
    def __init__(self, *a, **k): self.a, self.k = a, k

def rebuild_tensor(storage, offset, size, stride, *rest):
    return {"storage": storage, "offset": offset, "shape": list(size), "stride": list(stride)}

def rebuild_parameter(data, *rest): return data

class U(pickle.Unpickler):
    def find_class(self, mod, name):
        if mod == "torch._utils" and name in ("_rebuild_tensor_v2", "_rebuild_tensor"): return rebuild_tensor
        if mod == "torch._utils" and name == "_rebuild_parameter": return rebuild_parameter
        if mod == "torch" and name in DT: return Storage(name)
        if mod == "collections" and name == "OrderedDict":
            import collections; return collections.OrderedDict
        raise pickle.UnpicklingError("blocked %s.%s" % (mod, name))
    def persistent_load(self, pid):
        # ('storage', storage_type, key, location, numel)
        assert pid[0] == "storage"
        return {"key": pid[2], "dtype": pid[1].name, "numel": pid[4]}

path = sys.argv[1]
z = zipfile.ZipFile(path)
names = z.namelist()
pk = [n for n in names if n.endswith("data.pkl")][0]
prefix = pk[: -len("data.pkl")]
obj = U(z.open(pk)).load()
out = {"archive_prefix": prefix, "members": names, "tensors": []}
try:
    import torch; out["oracle_torch"] = torch.__version__
except Exception: pass
def walk(o, path=""):
    if isinstance(o, dict) and "storage" in o and "shape" in o:
        s = o["storage"]; d = DT[s["dtype"]]
        out["tensors"].append({"name": path, "member": "data/%s" % s["key"], "dtype": d[0], "width": d[1], "shape": o["shape"],
                               "offset": o["offset"], "numel": s["numel"]})
    elif isinstance(o, dict):
        for k, v in o.items(): walk(v, path + ("." if path else "") + str(k))
    elif isinstance(o, (list, tuple)):
        for i, v in enumerate(o): walk(v, path + "[%d]" % i)
walk(obj)
json.dump(out, sys.stdout)
