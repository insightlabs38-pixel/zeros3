#!/usr/bin/env python3
"""B6 inputs beyond the B5 corpus, Python standard library only (deterministic).
usage: make_inputs.py CORPUS_DIR
  go/compile-<v>.tar      go/src/cmd/compile of go<v>.src.tar.gz, members and headers in archive order
  cu/<path> x 3           Coreutils 9.5/9.6/9.7 src/ and lib/ members >= 4 KiB (per-file history family)
  bundle/{c,a,b}.tar      joint-selection workload: a and b ship the same vendored lib/ (9.7) plus their own
                          src/ halves; c (older) ships lib/ of 9.5"""
import os, sys, tarfile, io
C = sys.argv[1]
os.makedirs(f'{C}/go', exist_ok=True)
for v in ['1.22.0', '1.22.6', '1.23.0', '1.23.4']:
    out = f'{C}/go/compile-{v}.tar'
    if os.path.exists(out):
        continue
    with tarfile.open(f'{C}/go/go{v}.src.tar.gz') as src, tarfile.open(out + '.tmp', 'w', format=tarfile.PAX_FORMAT) as dst:
        for m in src:
            if m.name.startswith('go/src/cmd/compile/'):
                dst.addfile(m, src.extractfile(m) if m.isfile() else None)
    os.rename(out + '.tmp', out)
def members(v, pre):
    with tarfile.open(f'{C}/src/coreutils-{v}.tar') as t:
        return [(m, t.extractfile(m).read()) for m in t if m.isfile() and m.name.startswith(f'coreutils-{v}/{pre}')]
os.makedirs(f'{C}/cu', exist_ok=True)
for v in ['9.5', '9.6', '9.7']:
    d = f'{C}/cu/{v}'
    if os.path.exists(d + '/.done'):
        continue
    os.makedirs(d, exist_ok=True)
    for pre in ['src/', 'lib/']:
        for m, b in members(v, pre):
            if len(b) >= 4096:
                p = os.path.join(d, m.name.split('/', 1)[1])
                os.makedirs(os.path.dirname(p), exist_ok=True)
                open(p, 'wb').write(b)
    open(d + '/.done', 'w').close()
os.makedirs(f'{C}/bundle', exist_ok=True)
def bundle(name, parts):
    out = f'{C}/bundle/{name}.tar'
    if os.path.exists(out):
        return
    with tarfile.open(out, 'w', format=tarfile.USTAR_FORMAT) as t:
        for ms in parts:
            for m, b in ms:
                t.addfile(m, io.BytesIO(b))
lib97, lib95, src97 = members('9.7', 'lib/'), members('9.5', 'lib/'), members('9.7', 'src/')
half = len(src97) // 2
bundle('c', [lib95])
bundle('a', [lib97, src97[:half]])
bundle('b', [lib97, src97[half:]])
