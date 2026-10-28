"""Bounded Stage-A oracle: native legacy zstreamdump owns record interpretation.
No send-stream parser or snapshot/base decoder is added to repz3.
Usage: CACHE (q14-tools/q13-tools/q14-corpus must already exist).
"""
import hashlib,json,os,pathlib,re,struct,subprocess,sys
cache=pathlib.Path(sys.argv[1]);corpus=cache/'q14-corpus';env=dict(os.environ,LD_LIBRARY_PATH=f'{cache}/q13-tools/lib/x86_64-linux-gnu:{cache}/q13-tools/usr/lib/x86_64-linux-gnu')
inputs=[];pins=[]
def sha(b):return hashlib.sha256(b).hexdigest()
def native(p):
 b=p.read_bytes();assert 0<len(b)<=128<<20
 r=subprocess.run([str(cache/'q14-tools/sbin/zstreamdump'),'-v'],input=b,capture_output=True,env=env,timeout=30)
 text=r.stdout.decode();assert r.returncode==0 and 'INVALID' not in text and 'END checksum =' in text
 (p.with_suffix('.dump.txt')).write_text(text+r.stderr.decode())
 offset=0;records=[];payloads=[];counts={}
 lines=text.splitlines()
 for index,line in enumerate(lines):
  if not re.match(r'^(BEGIN record|END checksum|OBJECT object|FREEOBJECTS firstobj|WRITE object|FREE object)',line):continue
  kind=line.split()[0];counts[kind]=counts.get(kind,0)+1;size=0
  if kind=='OBJECT':size=(int(re.search(r'bonuslen = (\d+)',line)[1])+7)//8*8
  if kind=='WRITE':
   # The legacy native oracle prints length on the following line.
   following=lines[index+1];size=int(re.search(r'length = (\d+)',following)[1])
  assert offset+312+size<=len(b)
  records.append([offset,312+size])
  if size:payloads.append([offset+312,size])
  offset+=312+size
 assert offset==len(b) and counts['BEGIN']==counts['END']==1
 reported=int(re.search(r'Total stream length = (\d+)',text)[1]);assert reported==len(b)
 assert struct.unpack_from('<Q',b,8)[0]==0x2f5bacbac
 return dict(resource_spans=records,chunk_spans=payloads,toguid=struct.unpack_from('<Q',b,40)[0],fromguid=struct.unpack_from('<Q',b,48)[0],counts=counts)
meta={}
for p in sorted((corpus/'legacy').glob('*.zsend')):
 b=p.read_bytes();meta[p.name]=native(p);pins.append(dict(file=p.name,bytes=len(b),sha256=sha(b),**meta[p.name]))
assert (corpus/'legacy/full3.zsend').read_bytes()==(corpus/'legacy/full3-repeat.zsend').read_bytes()
for n in range(1,4):
 assert meta[f'incr{n}.zsend']['fromguid']==meta[f'full{n-1}.zsend']['toguid']
 assert meta[f'incr{n}.zsend']['toguid']==meta[f'full{n}.zsend']['toguid']
def add(family,p,stage,cls,extra=None):
 b=p.read_bytes();inputs.append(dict(family=family,path=str(p),file=p.name,sha256=sha(b),bytes=len(b),stage=stage,class_=cls,oracles=[],**(extra or {})))
for family,names in [('legacy-full-history',[f'full{n}.zsend' for n in range(4)]),('legacy-incremental-chain',['full0.zsend',*[f'incr{n}.zsend' for n in range(1,4)]]),('legacy-independent-compression',['full3.zsend','independent-off.zsend','independent-lzjb.zsend']),('legacy-identical-repeat',['full3.zsend','full3-repeat.zsend'])]:
 for name in names:add(family,corpus/'legacy'/name,name,'NATIVE_PRODUCER_WORKLOAD_NOT_NATURAL_HISTORY',meta[name])
for r in json.loads((corpus/'public.json').read_text()):
 name=r['decoded_file'];p=corpus/name
 assert p.stat().st_size==r['decoded_bytes'] and sha(p.read_bytes())==r['decoded_sha256']
 family='reference-btrfs' if r['format']=='btrfs' else 'reference-'+name.split('-all-drr-types-')[0]+'-'+name.split('-')[-1].split('.')[0]
 add(family,p,name,r['class_'])
(corpus/'inputs.json').write_text(json.dumps(inputs,indent=1));(corpus/'legacy-pins.json').write_text(json.dumps(pins,indent=1));print(json.dumps(dict(inputs=len(inputs),native_files=len(pins),unique_sources=len({r['sha256'] for r in inputs}))))
