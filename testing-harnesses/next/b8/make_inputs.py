"""B8 inputs derived from the fetched corpus C (run.sh fetches the published files):
  src/coreutils-9.{5,6,7}.tar        decoded from the GNU .tar.xz
  b8/cu-9.{6,7}.l3.tar.zst            python-zstandard level 3 of those tars
  b8/cu-9.{6,7}.l19.tar.zst           level 19 (replay-inadmissible control)
  b8/ts-5.6.3.tar                     the npm 5.6.3 .tgz decoded
  b8/ts-5.7.2.l3.tar.zst              the npm 5.7.2 .tgz decoded, zstd level 3
  random/r0..2.bin                    16 MiB random (B5 recipe)
Deterministic: zstd without checksum/content-size variation, fixed seeds."""
import gzip, lzma, os, random, sys
import zstandard

C = sys.argv[1]
os.makedirs(f'{C}/b8', exist_ok=True)
os.makedirs(f'{C}/random', exist_ok=True)


def once(path, make):
    if not os.path.exists(path):
        tmp = path + '.tmp'
        with open(tmp, 'wb') as f:
            f.write(make())
        os.replace(tmp, path)


for v in ['9.5', '9.6', '9.7']:
    once(f'{C}/src/coreutils-{v}.tar', lambda v=v: lzma.decompress(open(f'{C}/src/coreutils-{v}.tar.xz', 'rb').read()))
for v in ['9.6', '9.7']:
    for lvl in [3, 19]:
        once(f'{C}/b8/cu-{v}.l{lvl}.tar.zst',
             lambda v=v, lvl=lvl: zstandard.ZstdCompressor(level=lvl).compress(open(f'{C}/src/coreutils-{v}.tar', 'rb').read()))
once(f'{C}/b8/ts-5.6.3.tar', lambda: gzip.decompress(open(f'{C}/ts/typescript-5.6.3.tgz', 'rb').read()))
once(f'{C}/b8/ts-5.7.2.l3.tar.zst',
     lambda: zstandard.ZstdCompressor(level=3).compress(gzip.decompress(open(f'{C}/ts/typescript-5.7.2.tgz', 'rb').read())))
for i in range(3):
    once(f'{C}/random/r{i}.bin', lambda i=i: random.Random(500 + i).randbytes(16 << 20))
