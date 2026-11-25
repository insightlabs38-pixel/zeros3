"""Laboratory pack with per-scope dictionary ownership and bounded solid blocks."""
from collections import Counter, defaultdict
from contextlib import contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import tempfile
import time
import zlib

import zstandard as zstd


DICT_BYTES = 8192
BLOCK_BYTES = 32768
INDEX_MAGIC = b"C13INDEX1"
INDEX_LIMIT = 8 << 20
GROUP_BYTES = {"block": BLOCK_BYTES, "block8": 8192}
LANES = ("deflate", "prefix", "zstd", "trained", *GROUP_BYTES)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def allocation(path):
    return sum(p.stat().st_blocks * 512 for p in (path, *path.iterdir()))


@contextmanager
def locked(root, exclusive):
    with (root / "LOCK").open("a+b") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX if exclusive else fcntl.LOCK_SH)
        yield


def durable_directory(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def index_bytes(index):
    body = json.dumps(index, sort_keys=True, separators=(",", ":")).encode()
    return INDEX_MAGIC + hashlib.sha256(body).digest() + zlib.compress(body, 6)


def read_index(root, scope):
    payload = (root / "INDEX").read_bytes()
    if not payload.startswith(INDEX_MAGIC):
        raise ValueError("invalid index")
    at = len(INDEX_MAGIC)
    decoder = zlib.decompressobj()
    body = decoder.decompress(payload[at+32:], INDEX_LIMIT+1)
    if (not decoder.eof or decoder.unused_data or len(body) > INDEX_LIMIT
            or hashlib.sha256(body).digest() != payload[at:at+32]):
        raise ValueError("index identity differs")
    index = json.loads(body)
    if index["scope"] != scope:
        raise ValueError("archive scope differs")
    return index


def write_all(path, data):
    with path.open("wb") as f:
        f.write(data)
        f.flush()
        os.fsync(f.fileno())


def pread(fd, at, size):
    data = os.pread(fd, size, at)
    if len(data) != size:
        raise ValueError("pack extent is incomplete")
    return data


def dict_identity(scope, cohort, data):
    return sha(scope.encode() + b"\0" + cohort.encode() + b"\0" + data)


def dictionary_rows(index):
    counts = Counter(block[5] for block in index["blocks"].values() if block[5])
    if set(counts) != set(index["dicts"]):
        raise ValueError("dictionary closure differs")
    for key, entry in index["dicts"].items():
        if entry[5] != counts[key]:
            raise ValueError("dictionary reference count differs")


class Archive:
    def __init__(self, root, scope):
        self.root = root.resolve()
        self.fds, self.dictionaries = {}, {}
        try:
            with locked(root, False):
                self.index = read_index(root, scope)
                dictionary_rows(self.index)
                files = {row[0] for row in self.index["blocks"].values()}
                files.update(row[1] for row in self.index["dicts"].values())
                for name in files:
                    if Path(name).name != name or not name.startswith("pack-"):
                        raise ValueError("invalid pack name")
                    self.fds[name] = os.open(root / name, os.O_RDONLY)
            for ident, (cohort, pack, at, size, expected, _) in self.index["dicts"].items():
                if not 1 <= size <= DICT_BYTES:
                    raise ValueError("dictionary exceeds its bound")
                raw = pread(self.fds[pack], at, size)
                if sha(raw) != expected or dict_identity(scope, cohort, raw) != ident:
                    raise ValueError("dictionary identity differs")
                self.dictionaries[ident] = raw
        except BaseException:
            self.close()
            raise

    def close(self):
        for fd in self.fds.values():
            os.close(fd)
        self.fds.clear()

    def __enter__(self):
        return self

    def __exit__(self, *_):
        self.close()

    def get(self, key):
        frame, at, n, expected = self.index["objects"][key]
        pack, pos, stored, size, mode, ident = self.index["blocks"][frame]
        if not 1 <= size <= BLOCK_BYTES or not 1 <= stored <= size:
            raise ValueError("frame exceeds its bound")
        payload = pread(self.fds[pack], pos, stored)
        raw = self.dictionaries[ident] if ident else b""
        if mode == "raw":
            data = payload
        elif mode in ("deflate", "prefix"):
            decoder = zlib.decompressobj(wbits=-15, zdict=raw) if raw else zlib.decompressobj(wbits=-15)
            data = decoder.decompress(payload, size+1)
            if not decoder.eof or decoder.unused_data:
                raise ValueError("deflate frame extent differs")
        elif mode in ("zstd", "trained"):
            announced = zstd.frame_content_size(payload)
            if announced not in (-1, size):
                raise ValueError("zstd frame extent differs")
            dictionary = zstd.ZstdCompressionDict(raw) if raw else None
            data = zstd.ZstdDecompressor(dict_data=dictionary).decompress(payload, max_output_size=size)
        else:
            raise ValueError("unsupported pack codec")
        if len(data) != size or at < 0 or n < 1 or at+n > size:
            raise ValueError("object extent differs")
        result = data[at:at+n]
        if sha(result) != expected:
            raise ValueError("object digest differs")
        return result


def train(entries, mode):
    if mode not in ("prefix", "trained"):
        return {}
    training = defaultdict(dict)
    for key, data in sorted(entries.items()):
        cohort, split, _ = key.split("/")
        if split == "train":
            training[cohort][sha(data)] = data
    dictionaries = {}
    for cohort, samples in sorted(training.items()):
        data = list(samples.values())
        if mode == "prefix":
            dictionaries[cohort] = b"".join(data[-16:])[-DICT_BYTES:]
        elif mode == "trained" and len(data) >= 8:
            try:
                dictionaries[cohort] = zstd.train_dictionary(DICT_BYTES, data).as_bytes()
            except zstd.ZstdError:
                # A small or insufficiently varied training cohort uses the independent baseline.
                continue
    return dictionaries


def packed_groups(unique, mode):
    cap = GROUP_BYTES.get(mode)
    if cap is None:
        for key, data in unique:
            yield [(key, data)]
        return
    group, size, cohort = [], 0, ""
    for key, data in unique:
        next_cohort = key.split("/")[0]
        if group and (cohort != next_cohort or size+len(data) > cap):
            yield group
            group, size = [], 0
        cohort = next_cohort
        group.append((key, data))
        size += len(data)
    if group:
        yield group


def compress(data, mode, raw):
    if mode in ("zstd", "trained"):
        base = zstd.ZstdCompressor(level=6, write_content_size=False).compress(data)
        if mode == "trained" and raw:
            dictionary = zstd.ZstdCompressionDict(raw)
            reference = zstd.ZstdCompressor(level=6, dict_data=dictionary, write_content_size=False)
            trial = reference.compress(data)
        else:
            trial = base
        codec = "zstd"
    else:
        codec = "deflate"
        base = zlib.compress(data, level=6, wbits=-15)
        if mode == "prefix" and raw:
            encoder = zlib.compressobj(level=6, wbits=-15, zdict=raw)
            trial = encoder.compress(data) + encoder.flush()
        else:
            trial = base
    if len(trial) < min(len(data), len(base)):
        return trial, mode, True
    if len(base) < len(data):
        return base, codec, False
    return data, "raw", False


def publish(root, scope, mode, new, previous=None, delete=(), rebuild=False, cut_before_index=False):
    root.mkdir(exist_ok=True)
    prior = previous.index if previous else {}
    if previous and (previous.root != root.resolve() or prior["scope"] != scope or prior["mode"] != mode):
        raise ValueError("publication base differs")
    epoch = prior.get("epoch", 0) + 1
    name = f"pack-{epoch:06}.bin"
    retained = {} if rebuild else prior
    index = {"scope": scope, "epoch": epoch, "mode": mode,
             "objects": {key: row for key, row in retained.get("objects", {}).items() if key not in delete},
             "blocks": dict(retained.get("blocks", {})),
             "dicts": {key: list(row) for key, row in retained.get("dicts", {}).items()}}
    objects = index["objects"]
    exact = {row[3]: row for row in objects.values()}
    aliases = defaultdict(list)
    unique = []
    for key, data in sorted(new.items()):
        digest = sha(data)
        if key in objects or not 1 <= len(data) <= BLOCK_BYTES:
            raise ValueError("invalid incoming object")
        if digest in exact:
            objects[key] = exact[digest]
        else:
            if not aliases[digest]:
                unique.append((key, data))
            aliases[digest].append(key)
    # Detached staging lets readers open the active index throughout encoding.
    start = time.process_time()
    if previous and not rebuild:
        dictionaries = {row[0]: previous.dictionaries[ident] for ident, row in index["dicts"].items()}
    else:
        dictionaries = train(new, mode)
    training_cpu = time.process_time() - start
    with tempfile.TemporaryDirectory(prefix=".staging-", dir=root) as temp:
        staging = Path(temp)
        path = staging / name
        with path.open("wb") as out:
            for seq, group in enumerate(packed_groups(unique, mode)):
                cohort = group[0][0].split("/")[0]
                data = b"".join(payload for _, payload in group)
                raw = dictionaries.get(cohort, b"")
                packed, codec, uses_dict = compress(data, mode, raw)
                ident = ""
                if uses_dict:
                    ident = dict_identity(scope, cohort, raw)
                    if ident not in index["dicts"]:
                        index["dicts"][ident] = [cohort, name, out.tell(), len(raw), sha(raw), 0]
                        out.write(raw)
                frame = f"{epoch}:{seq}"
                index["blocks"][frame] = [name, out.tell(), len(packed), len(data), codec, ident]
                out.write(packed)
                at = 0
                for _, payload in group:
                    digest = sha(payload)
                    row = [frame, at, len(payload), digest]
                    for key in aliases[digest]:
                        objects[key] = row
                    at += len(payload)
            out.flush()
            os.fsync(out.fileno())
        staged_bytes = path.stat().st_size
        live = {row[0] for row in objects.values()}
        index["blocks"] = {key: row for key, row in index["blocks"].items() if key in live}
        counts = Counter(row[5] for row in index["blocks"].values() if row[5])
        index["dicts"] = {key: row for key, row in index["dicts"].items() if key in counts}
        for ident, count in counts.items():
            index["dicts"][ident][5] = count
        dictionary_rows(index)
        metadata = index_bytes(index)
        write_all(staging / "INDEX", metadata)
        with locked(root, True):
            current = read_index(root, scope) if (root / "INDEX").exists() else {}
            if current.get("epoch", 0) != prior.get("epoch", 0):
                raise ValueError("archive publication is stale")
            if staged_bytes:
                os.replace(path, root / name)
                durable_directory(root)
            if cut_before_index:
                raise InterruptedError("cut before index publication")
            os.replace(staging / "INDEX", root / "INDEX")
            durable_directory(root)
            referenced = {row[0] for row in index["blocks"].values()}
            referenced.update(row[1] for row in index["dicts"].values())
            for path in root.glob("pack-*.bin"):
                if path.name not in referenced:
                    path.unlink()
            durable_directory(root)
    owners = {}
    for key, row in sorted(objects.items()):
        owners.setdefault(row[0], key.split("/")[0])
    cohorts = defaultdict(Counter)
    for frame, row in index["blocks"].items():
        summary = cohorts[owners[frame]]
        summary["frames"] += 1
        summary["payload_bytes"] += row[2]
        summary["logical_bytes"] += row[3]
        summary["raw_frames"] += row[4] == "raw"
    for row in index["dicts"].values():
        cohorts[row[0]]["dictionary_bytes"] += row[3]
    return {"allocated": allocation(root), "index_bytes": len(metadata),
            "index_allocated": (root/"INDEX").stat().st_blocks*512, "staged_pack_bytes": staged_bytes,
            "physical_pack_bytes": sum(p.stat().st_size for p in root.glob("pack-*.bin")),
            "dictionary_bytes": sum(row[3] for row in index["dicts"].values()),
            "dictionary_references": sum(counts.values()), "dictionaries": len(counts),
            "live_block_payload_bytes": sum(row[2] for row in index["blocks"].values()),
            "logical_frame_bytes": sum(row[3] for row in index["blocks"].values()),
            "frames_by_codec": dict(Counter(row[4] for row in index["blocks"].values())),
            "cohorts": cohorts,
            "unique_frames": len(index["blocks"]), "objects": len(objects),
            "training_cpu_seconds": training_cpu}
