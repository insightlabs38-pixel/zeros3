"""B8-A reference oracle, independent of the engine (Python standard library + python-zstandard for .zst).
For each family: container bytes, decoded bytes, distinct decoded streams (byte-stream equivalence), member
contents across all containers (tar and ZIP entries keyed by SHA-256 of their bytes: structural equivalence),
and two storage bounds priced with zlib level 6 per unit (the pack writer's level):
  stream_bound  every distinct decoded stream compressed once (whole-stream sharing only)
  member_bound  every distinct member content compressed once (member sharing, no deltas, no headers)
usage: oracle.py CORPUS_DIR > oracle.jsonl"""
import gzip, hashlib, io, json, lzma, sys, tarfile, zipfile, zlib
import zstandard

C = sys.argv[1]
FAMS = {
    'ts': ['ts/typescript-5.6.2.tgz', 'ts/typescript-5.6.3.tgz', 'ts/typescript-5.7.2.tgz'],
    'django': [f'django/Django-{v}{s}' for v in ['5.1.1', '5.1.2', '5.1.3'] for s in ['.tar.gz', '-py3-none-any.whl']],
    'cu-gz': [f'src/coreutils-{v}.tar.gz' for v in ['9.5', '9.6', '9.7']],
    'wrap': ['src/coreutils-9.7.tar.gz', 'src/coreutils-9.7.tar', 'b8/cu-9.7.l3.tar.zst', 'b8/cu-9.6.l3.tar.zst'],
    'ts-mix': ['ts/typescript-5.6.2.tgz', 'b8/ts-5.6.3.tar', 'b8/ts-5.7.2.l3.tar.zst'],
}


def decode(p, raw):
    if raw[:2] == b'\x1f\x8b':
        return gzip.decompress(raw)
    if raw[:4] == b'\x28\xb5\x2f\xfd':
        return zstandard.ZstdDecompressor().decompress(raw, max_output_size=1 << 30)
    if raw[:6] == b'\xfd7zXZ\x00':
        return lzma.decompress(raw)
    return raw


def members(p, raw, plain):
    if raw[:4] == b'PK\x03\x04':
        z = zipfile.ZipFile(io.BytesIO(raw))
        return [z.read(i) for i in z.infolist() if not i.is_dir()]
    t = tarfile.open(fileobj=io.BytesIO(plain))
    return [t.extractfile(m).read() for m in t.getmembers() if m.isfile()]


for fam, files in FAMS.items():
    comp = dec = 0
    streams, mem = {}, {}
    for f in files:
        raw = open(f'{C}/{f}', 'rb').read()
        comp += len(raw)
        plain = decode(f, raw) if raw[:4] != b'PK\x03\x04' else None
        if plain is not None:
            dec += len(plain)
            streams.setdefault(hashlib.sha256(plain).digest(), plain)
        for m in members(f, raw, plain):
            if plain is None:
                dec += len(m)
            mem.setdefault(hashlib.sha256(m).digest(), m)
    zips = [f for f in files if f.endswith('.whl')]
    stream_bound = sum(len(zlib.compress(s, 6)) for s in streams.values()) + sum(len(open(f'{C}/{f}', 'rb').read()) for f in zips)
    member_bound = sum(len(zlib.compress(m, 6)) for m in mem.values())
    row = {'kind': 'oracle', 'family': fam, 'containers': len(files), 'container_bytes': comp, 'decoded_bytes': dec,
           'distinct_streams': len(streams), 'distinct_members': len(mem), 'distinct_member_bytes': sum(map(len, mem.values())),
           'stream_bound': stream_bound, 'member_bound': member_bound}
    print(json.dumps(row), flush=True)
