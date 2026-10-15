"""Shared helpers for the Z3-18 deterministic producers.

Layout written under $Z3_STRUCT_CACHE (default ~/.cache/z3-realworld/structured):
  <lane>/<family>/<rev>/<files...>   and   <lane>/<family>/meta.json
Every variant is derived from r0 independently (cost = revision on a stored base)."""
import json, os, shutil, sys, platform
import numpy as np

SEED = 18


def root():
    return os.environ.get("Z3_STRUCT_CACHE") or os.path.join(os.path.expanduser("~"), ".cache", "z3-realworld", "structured")


def rng(*salt):
    s = SEED
    for x in salt:
        s = (s * 1000003 + sum(map(ord, str(x))) + 7) & 0x7FFFFFFF
    return np.random.RandomState(s)


class Family:
    def __init__(self, lane, name, producer):
        self.dir = os.path.join(root(), lane, name)
        shutil.rmtree(self.dir, ignore_errors=True)
        os.makedirs(self.dir)
        self.meta = {"lane": lane, "family": name, "seed": SEED, "producer": dict(producer, python=platform.python_version()), "revisions": []}

    def rev(self, name):
        d = os.path.join(self.dir, name)
        os.makedirs(d, exist_ok=True)
        return d

    def record(self, name, mutation, logical_bytes, note="", base="r0"):
        self.meta["revisions"].append({"rev": name, "mutation": mutation, "logical_bytes": int(logical_bytes), "note": note, "base": base})

    def close(self):
        with open(os.path.join(self.dir, "meta.json"), "w") as f:
            json.dump(self.meta, f, indent=1)
        print("wrote", self.dir, len(self.meta["revisions"]), "revisions", file=sys.stderr)
