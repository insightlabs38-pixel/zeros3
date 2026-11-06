#!/usr/bin/env python3
"""Build the B3 Git/tar/OCI corpus from pinned revisions of this repository.
Producers: git archive (tar), gzip CLI, Python tarfile+zlib (docker-like normalized layer),
git pack-objects (delta and --window=0), git hash-object (loose). Usage: make_git_corpus.py REPO OUT REV..."""
import gzip, io, os, shutil, subprocess, sys, tarfile, zlib

repo, out, revs = sys.argv[1], sys.argv[2], sys.argv[3:]
PATHS = ['.', ':(exclude)testing-harnesses', ':(exclude)docs']
os.makedirs(out, exist_ok=True)

def git(*a, inp=None, cwd=repo):
    return subprocess.run(['git', *a], cwd=cwd, input=inp, check=True, capture_output=True).stdout

for i, rev in enumerate(revs):
    full = git('rev-parse', rev).decode().strip()
    tag = f'r{i}_{full[:7]}'
    tar = git('archive', '--format=tar', full, '--', *PATHS)
    open(f'{out}/{tag}.tar', 'wb').write(tar)
    # gzip CLI level 6 (zlib) and level 9
    for lvl in (6, 9):
        p = subprocess.run(['gzip', f'-{lvl}', '-n', '-c'], input=tar, capture_output=True, check=True)
        open(f'{out}/{tag}.gz{lvl}.tar.gz', 'wb').write(p.stdout)
    # docker-like normalized layer: uid/gid 0, mtime 0, ustar, python zlib level 6
    src = tarfile.open(fileobj=io.BytesIO(tar))
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode='w', format=tarfile.USTAR_FORMAT) as dst:
        for m in src.getmembers():
            if not m.isreg():
                continue
            m.uid = m.gid = 0; m.uname = m.gname = ''; m.mtime = 0; m.mode = 0o644
            dst.addfile(m, src.extractfile(m))
    layer = buf.getvalue()
    gzb = io.BytesIO()
    with gzip.GzipFile(fileobj=gzb, mode='wb', compresslevel=6, mtime=0) as g:
        g.write(layer)
    open(f'{out}/{tag}.layer.tar.gz', 'wb').write(gzb.getvalue())
    # git packs of the revision's objects
    objs = git('rev-list', '--objects', full, '--', *PATHS)
    ids = b''.join(l.split(b' ')[0] + b'\n' for l in objs.splitlines())
    for name, args in (('delta', ['--window=10']), ('nodelta', ['--window=0', '--depth=0'])):
        pk = git('pack-objects', '--stdout', *args, inp=ids)
        open(f'{out}/{tag}.{name}.pack', 'wb').write(pk)
    # loose objects: every 4th blob via a fresh repository
    tmp = f'{out}/_loose_{tag}'
    shutil.rmtree(tmp, ignore_errors=True)
    subprocess.run(['git', 'init', '-q', tmp], check=True)
    blobs = [l.split(b' ')[0].decode() for l in objs.splitlines()]
    n = 0
    for b in blobs:
        t = git('cat-file', '-t', b).decode().strip()
        if t != 'blob' or n >= 24:
            continue
        data = git('cat-file', 'blob', b)
        if len(data) < 64:
            continue
        sha = subprocess.run(['git', 'hash-object', '-w', '--stdin'], cwd=tmp, input=data, capture_output=True, check=True).stdout.decode().strip()
        shutil.copy(f'{tmp}/.git/objects/{sha[:2]}/{sha[2:]}', f'{out}/{tag}.loose.{sha[:8]}')
        n += 1
    shutil.rmtree(tmp)
    print(tag, len(tar), 'objects', len(blobs))
