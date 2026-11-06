#!/usr/bin/env python3
"""Inventory cached B1 artifacts and create deterministic controls; no producers."""
import argparse,glob,hashlib,json,pathlib,random
p=argparse.ArgumentParser();p.add_argument('--work',default='/workspace/work');a=p.parse_args();w=pathlib.Path(a.work);out=w/'z4-b1';out.mkdir(exist_ok=True);families=[]
def add(name,kind,paths):
 paths=[str(x) for x in paths]
 if len(paths)<1 or not all(pathlib.Path(x).is_file() for x in paths):raise RuntimeError((name,paths))
 families.append(dict(Name=name,Kind=kind,Paths=paths))
add('clr-newtonsoft','REAL_RELEASE',[w/'q6-public/clr'/f'{v}.dll' for v in ['12.0.3','13.0.1','13.0.2','13.0.3','13.0.4']])
add('dex-fdroid','REAL_RELEASE',[w/'q6-public/fdroid-apk'/f'{v}.dex' for v in ['2.0.0','2.0.1','2.0.2']])
add('model-pythia','REAL_CHECKPOINT',[w/'q6-public/pythia-14m'/f'step{v}.safetensors' for v in [1000,2000,3000]])
add('bitcode-hiredis','REAL_RELEASE_SOURCE_COMPILED',[w/'q15-corpus/bitcode/hiredis-hiredis.c-O0'/f'v{v}.bc' for v in ['1.0.0','1.1.0','1.2.0','1.3.0','1.4.0']])
add('elf-controlled','PRODUCER_WORKLOAD',[w/'q4/corpus'/f'rev{v}'/'gcc-dwarf5' for v in range(3)])
add('pe-controlled','PRODUCER_WORKLOAD',[w/'q4/corpus'/f'rev{v}'/'sample.exe' for v in range(3)])
add('parquet-chain','PRODUCER_WORKLOAD',[w/'q8-parquet/parquet/pyarrow-chain'/f'c{v}'/'db.parquet' for v in range(4)])
for engine in ['rocksdb','pebble']:
 paths=[]
 for v in range(4):paths.extend(sorted((w/f'q6-sst/{engine}/chain/c{v}').glob('*.sst')))
 add(engine+'-sst','PRODUCER_WORKLOAD',paths)
add('oci-transport','REAL_RELEASE_COMPRESSED_TRANSPORT',[w/'q6-producers'/f'oci-{v}.zst' for v in ['1.36.1','1.37.0']])
add('source-archives','REAL_RELEASE',[w/'q11-download'/f'coreutils-{v}.tar' for v in ['9.5','9.6','9.7']])
add('git-repack','SAME_HISTORY_REENCODING',[pathlib.Path('/workspace/zeros3/testing-harnesses/z3/gitz3/testdata')/f'sha1-{v}.pack' for v in ['v1','ref','ofs']])
# Distinct branches, sparse edits, insertions, moved regions, high entropy and
# deliberately similar edges. These are controls, not real workload evidence.
r=random.Random(811);base=r.randbytes(256*1024);b=bytearray(base)
for i in range(100, len(b),701):b[i]^=11
variants=[base,bytes(b),base[:12000]+b'inserted'*300+base[12000:],base[100000:]+base[:100000],r.randbytes(len(base)),base[:32]+r.randbytes(len(base)-64)+base[-32:]]
paths=[]
for i,b in enumerate(variants):q=out/f'control-{i}.bin';q.write_bytes(b);paths.append(q)
add('binary-controls','SYNTHETIC_RELATIONSHIP_AND_MISLEADING',paths)
# Minimal valid Wasm modules from fixed section bytes; no compiler claims.
paths=[]
for i in range(3):q=out/f'control-{i}.wasm';q.write_bytes(b'\0asm\1\0\0\0'+bytes([0,4,1,120,i,0]));paths.append(q)
add('wasm-control','SYNTHETIC_CUSTOM_SECTION',paths)
# Preserve existing tiny GGUF model fixture as a first-sight negative, explicitly
# not a model history. Real checkpoint evidence comes from Pythia above.
add('gguf-first-sight','PINNED_TINY_FIXTURE',[pathlib.Path('/workspace/zeros3/testing-harnesses/z4/testdata/a2-model/model.gguf')])
(out/'manifest.json').write_text(json.dumps(families,indent=2)+'\n')
inv=[]
for f in families:
 for path in f['Paths']:
  q=pathlib.Path(path);h=hashlib.sha256(q.read_bytes()).hexdigest();inv.append(dict(family=f['Name'],kind=f['Kind'],path=path,bytes=q.stat().st_size,sha256=h))
(out/'inventory.json').write_text(json.dumps(inv,indent=2)+'\n');print(len(families),'families',len(inv),'inputs',sum(x['bytes'] for x in inv),'bytes')
