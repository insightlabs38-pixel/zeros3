#!/usr/bin/env python3
"""B9 inputs (Python standard library only, deterministic).
usage: make_inputs.py CORPUS_DIR
  cu/<v>/...        Coreutils 9.5-9.7 src/ and lib/ members >= 4 KiB (B6's per-file history), from src/coreutils-<v>.tar
  gofiles/<v>/...   Go 1.22.0, 1.22.6, 1.23.0, 1.23.4 src/cmd/compile and src/go .go files of 4-256 KiB, from go/go<v>.src.tar.gz
  rnd/<v>/...       400 unrelated random files of 4-40 KiB per version (negative control)
  elf/<v>/<tool>    Go linux/amd64 toolchain executables (bin/go, pkg/tool/linux_amd64/{compile,link,asm,vet,cgo})
  pe/<v>/<tool>.exe the same tools of the windows/amd64 distribution"""
import os, sys, tarfile, zipfile, random
C = sys.argv[1]
def done(d): return os.path.exists(d + '/.done')
def mark(d): os.makedirs(d, exist_ok=True); open(d + '/.done', 'w').close()
for v in ['9.5', '9.6', '9.7']:
    d = f'{C}/cu/{v}'
    if done(d): continue
    with tarfile.open(f'{C}/src/coreutils-{v}.tar') as t:
        for m in t:
            if m.isfile() and any(m.name.startswith(f'coreutils-{v}/{p}') for p in ['src/', 'lib/']):
                b = t.extractfile(m).read()
                if len(b) >= 4096:
                    p = os.path.join(d, m.name.split('/', 1)[1]); os.makedirs(os.path.dirname(p), exist_ok=True); open(p, 'wb').write(b)
    mark(d)
GO = ['1.22.0', '1.22.6', '1.23.0', '1.23.4']
for v in GO:
    d = f'{C}/gofiles/{v}'
    if done(d): continue
    with tarfile.open(f'{C}/go/go{v}.src.tar.gz') as t:
        for m in t:
            n = m.name
            if m.isfile() and n.endswith('.go') and (n.startswith('go/src/cmd/compile/') or n.startswith('go/src/go/')) and 4096 <= m.size <= 262144:
                p = os.path.join(d, n.split('/', 1)[1]); os.makedirs(os.path.dirname(p), exist_ok=True); open(p, 'wb').write(t.extractfile(m).read())
    mark(d)
for i, v in enumerate(['0', '1', '2']):
    d = f'{C}/rnd/{v}'
    if done(d): continue
    r = random.Random(900 + i)
    for f in range(400):
        p = f'{d}/f{f:03d}.bin'; os.makedirs(d, exist_ok=True); open(p, 'wb').write(r.randbytes(4096 + r.randrange(36 * 1024)))
    mark(d)
TOOLS = ['pkg/tool/linux_amd64/compile', 'pkg/tool/linux_amd64/link', 'pkg/tool/linux_amd64/asm', 'pkg/tool/linux_amd64/vet', 'pkg/tool/linux_amd64/cgo', 'bin/go']
for v in GO:
    d = f'{C}/elf/{v}'
    if not done(d) and os.path.exists(f'{C}/gobin/go{v}.linux-amd64.tar.gz'):
        with tarfile.open(f'{C}/gobin/go{v}.linux-amd64.tar.gz') as t:
            for m in t:
                for tool in TOOLS:
                    if m.name == 'go/' + tool:
                        os.makedirs(d, exist_ok=True); open(f'{d}/{os.path.basename(tool)}', 'wb').write(t.extractfile(m).read())
        mark(d)
    d = f'{C}/pe/{v}'
    if not done(d) and os.path.exists(f'{C}/gobin/go{v}.windows-amd64.zip'):
        with zipfile.ZipFile(f'{C}/gobin/go{v}.windows-amd64.zip') as z:
            for tool in TOOLS:
                n = 'go/' + tool.replace('linux_amd64', 'windows_amd64') + '.exe'
                os.makedirs(d, exist_ok=True); open(f'{d}/{os.path.basename(n)}', 'wb').write(z.read(n))
        mark(d)
# synth/<v>/...: compact scale test, 4,000 text files of 4-12 KiB, three versions with three small edits each
W = [''.join(random.Random(w).choice('abcdefghijklmnopqrstuvwxyz') for _ in range(2 + w % 9)) for w in range(5000)]
prev = {}
for i, v in enumerate(['0', '1', '2']):
    d = f'{C}/synth/{v}'
    if done(d):
        continue
    for f in range(4000):
        r = random.Random(f * 7 + i)
        if i == 0:
            g = random.Random(f); n = 4096 + g.randrange(8192)
            b = bytearray(); 
            while len(b) < n: b += (W[g.randrange(5000)] + (' ' if g.random() < .9 else '\n')).encode()
            b = bytes(b[:n])
        else:
            b = bytearray(prev[f])
            for _ in range(3):
                o = r.randrange(len(b) - 32); b[o:o+12] = (f'v{i}e{r.randrange(10**8):08d}' + ' ').encode()[:12]
            b = bytes(b)
        prev[f] = b
        p = f'{d}/d{f % 40:02d}/f{f:04d}.txt'; os.makedirs(os.path.dirname(p), exist_ok=True); open(p, 'wb').write(b)
    mark(d)
