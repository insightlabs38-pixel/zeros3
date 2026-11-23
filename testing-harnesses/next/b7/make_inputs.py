#!/usr/bin/env python3
"""B7 inputs beyond the B6 corpus, Python standard library only (deterministic).
usage: make_inputs.py CORPUS_DIR
  gof/<v>/<path>         go/src/cmd/compile members >= 4 KiB of 1.22.0/1.22.6/1.23.0/1.23.4 (per-file history)
  vendor/p<k>-r<1|2>.tar six products, two releases: r1 = lib/ of 9.5 + the k-th sixth of src/ 9.5,
                         r2 = lib/ of 9.7 + the k-th sixth of src/ 9.7 (vendored content shared across products,
                         own code shared only with the product's previous release)
  tensors/t<i>.bin       float32 checkpoint-like history: t1 retrains 3 of 24 tensors and perturbs 1% of the
                         values of 2 more, t2 inserts a tensor and swaps two (shifted, reordered regions)
  unrelated/*            one member of each unrelated family (negative control)"""
import os, sys, tarfile, io, random, struct, json, shutil
C = sys.argv[1]
def done(d):
    return os.path.exists(d + '/.done')
for v in ['1.22.0', '1.22.6', '1.23.0', '1.23.4']:
    d = f'{C}/gof/{v}'
    if done(d):
        continue
    os.makedirs(d, exist_ok=True)
    with tarfile.open(f'{C}/go/compile-{v}.tar') as t:
        for m in t:
            if m.isfile() and m.size >= 4096:
                p = os.path.join(d, m.name.split('/', 3)[3])
                os.makedirs(os.path.dirname(p), exist_ok=True)
                open(p, 'wb').write(t.extractfile(m).read())
    open(d + '/.done', 'w').close()
def members(v, pre):
    with tarfile.open(f'{C}/src/coreutils-{v}.tar') as t:
        return [(m, t.extractfile(m).read()) for m in t if m.isfile() and m.name.startswith(f'coreutils-{v}/{pre}')]
os.makedirs(f'{C}/vendor', exist_ok=True)
if not done(f'{C}/vendor'):
    for v, r in [('9.5', 1), ('9.7', 2)]:
        lib, src = members(v, 'lib/'), members(v, 'src/')
        for k in range(6):
            part = src[k * len(src) // 6:(k + 1) * len(src) // 6]
            with tarfile.open(f'{C}/vendor/p{k}-r{r}.tar', 'w', format=tarfile.USTAR_FORMAT) as t:
                for m, b in lib + part:
                    t.addfile(m, io.BytesIO(b))
    open(f'{C}/vendor/.done', 'w').close()
os.makedirs(f'{C}/tensors', exist_ok=True)
if not done(f'{C}/tensors'):
    rng = random.Random(7)
    def tensor(n):  # gaussian float32 weights: incompressible mantissas, structured exponents
        return [rng.gauss(0, 0.02) for _ in range(n)]
    sizes = [rng.choice([1 << 17, 1 << 18, 3 << 17]) for _ in range(24)]
    ts = [(f'layer{i}.w', tensor(n)) for i, n in enumerate(sizes)]
    def write(path, ts):
        hdr, off = {}, 0
        for name, vals in ts:
            hdr[name] = {'dtype': 'F32', 'shape': [len(vals)], 'offsets': [off, off + 4 * len(vals)]}
            off += 4 * len(vals)
        h = json.dumps(hdr, sort_keys=True).encode()
        with open(path, 'wb') as f:
            f.write(struct.pack('<Q', len(h)) + h)
            for _, vals in ts:
                f.write(struct.pack(f'<{len(vals)}f', *vals))
    write(f'{C}/tensors/t0.bin', ts)
    t1 = list(ts)
    for i in (5, 13, 21):
        t1[i] = (t1[i][0], tensor(len(t1[i][1])))
    for i in (8, 17):
        vals = list(t1[i][1])
        for j in range(0, len(vals), 100):
            vals[j + rng.randrange(100) if j + 100 <= len(vals) else j] += rng.gauss(0, 0.001)
        t1[i] = (t1[i][0], vals)
    write(f'{C}/tensors/t1.bin', t1)
    t2 = list(t1)
    t2.insert(12, ('adapter.w', tensor(1 << 17)))
    t2[2], t2[19] = t2[19], t2[2]
    write(f'{C}/tensors/t2.bin', t2)
    open(f'{C}/tensors/.done', 'w').close()
os.makedirs(f'{C}/unrelated', exist_ok=True)
for src, dst in [('src/coreutils-9.7.tar', 'coreutils.tar'), ('go/compile-1.23.4.tar', 'compile.tar'),
                 ('sqlite/v3.db', 'app.db'), ('ts/typescript-5.7.2.tgz', 'ts.tgz'), ('git/h2.pack', 'h.pack'),
                 ('random/r0.bin', 'r.bin')]:
    if not os.path.exists(f'{C}/unrelated/{dst}'):
        shutil.copyfile(f'{C}/{src}', f'{C}/unrelated/{dst}')
