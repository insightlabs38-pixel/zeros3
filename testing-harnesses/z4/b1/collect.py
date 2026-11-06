#!/usr/bin/env python3
"""Verify accounting/closure and preserve compact raw B1 evidence."""
import argparse,base64,collections,glob,gzip,hashlib,json,pathlib,struct,subprocess
p=argparse.ArgumentParser();p.add_argument('--work',default='/workspace/work/z4-b1');p.add_argument('--out',default='testing-harnesses/z4/b1');a=p.parse_args();w=pathlib.Path(a.work);out=pathlib.Path(a.out);out.mkdir(exist_ok=True)
def rows(path):
 return [json.loads(l) for l in pathlib.Path(path).read_text().splitlines() if l.strip()]
def varint(b,i):
 v=0;s=0
 while True:
  c=b[i];i+=1;v|=(c&127)<<s;s+=7
  if not c&128:return v,i
  assert s<70
def header(b):
 assert b[:4]==b'B1D1';n,i=varint(b,5);h=b[i:i+32].hex();i+=32;count=b[i];i+=1;bases=[]
 for _ in range(count):bases.append(b[i:i+32].hex());i+=32;_,i=varint(b,i)
 return h,n,bases
normal=[];components=[];closures=[]
for mode,dest in [('cdc',normal),('components',components)]:
 for path in sorted(w.glob('*-'+mode+'.jsonl')):
  batch=rows(path)
  if len(batch)!=1:continue
  x=batch[0]
  if path.name!=x['Family']+'-'+mode+'.jsonl':continue
  assert x['Authoritative']==x['Standalone']+x['IndexBytes'];assert x['Additive']==x['CDC']+x['Authoritative'];assert x['Migration']==x['Additive'];assert x['MaxDepth']<=2
  if x['Stages']:assert x['Stages'][-1]['Standalone']==x['Standalone']
  store=pathlib.Path(x['Archive']);files={q.stem:q.read_bytes() for q in store.glob('*.delta')};assert len(files)==x['Unique'];assert sum(len(b)+96 for b in files.values())+x['RootBytes']==x['Standalone']
  heads={h:header(b) for h,b in files.items()};assert all(h==v[0] for h,v in heads.items());fan=collections.Counter(len(v[2]) for v in heads.values());depth={}
  def walk(h,active):
   assert h in heads and h not in active
   if h not in depth:depth[h]=max([0]+[1+walk(b,active|{h}) for b in heads[h][2]])
   return depth[h]
  for h in heads:walk(h,set())
  assert max(depth.values(),default=0)==x['MaxDepth'];assert sum(v for k,v in fan.items() if k>1)==x['Multi']
  dest.append(x);closures.append(dict(family=x['Family'],mode=mode,objects=len(heads),max_depth=max(depth.values(),default=0),fanout=dict(fan),wire_bytes=sum(map(len,files.values()))))
assert len(normal)==16 and len(components)==10,(len(normal),len(components))
evidence=dict(normal=normal,components=components,closures=closures,inventory=json.load(open(w/'inventory.json')))
patterns=['scale-final-*.json','hash-*.json','quality-final.jsonl','numeric-v4.jsonl','structural.jsonl','extra-structural.jsonl','aggregation.jsonl','depth?.jsonl','bench-*.jsonl','*-cdc.replay.jsonl','*-components.replay.jsonl','model-cold-qualified.jsonl','git-structural.jsonl']
for pattern in patterns:
 for path in sorted(w.glob(pattern)):evidence[path.name]=rows(path)
for path in sorted(w.glob('*.time')):evidence[path.name]=path.read_text()
for path in sorted(w.glob('*tests.log'))+sorted(w.glob('*vet.log'))+sorted(w.glob('*fuzz.log'))+sorted(w.glob('*race.log')):evidence[path.name]=path.read_text()
evidence['diagnostics']={'initial_measurement_launch':'/usr/bin/time unavailable (exit127); replaced by isolated resource.getrusage child wrapper; no PASS inferred','model_cold_pre_no_base_control':rows(w/'model-cold.jsonl'),'model_cold_with_no_base_control':rows(w/'model-cold-v4.jsonl')}
evidence['accounting-before-repair.log']=(w/'accounting-before-repair.log').read_text()
evidence['scratch-isolation.json']=json.load(open(w/'scratch-isolation.json'))
evidence['source_sha256']={str(p):hashlib.sha256(p.read_bytes()).hexdigest() for folder in ['testing-harnesses/z3/simdelta','testing-harnesses/z3/cmd/z4similarity'] for p in pathlib.Path(folder).glob('*.go')}
(out/'evidence.json.gz').write_bytes(gzip.compress(json.dumps(evidence,sort_keys=True,separators=(',',':')).encode(),mtime=0))
# A pinned real CLR revision, complete root list and all dependency closure bytes,
# without shipping the whole public corpus. Replayed by Go unit tests offline.
store=w/'stores/clr-newtonsoft-cdc';m=json.load(open(store/'roots.json'));roots=[bytes(x).hex() for x in m['Roots'][-1]];objects={}
def collect(h):
 if h in objects:return
 b=(store/(h+'.delta')).read_bytes();objects[h]=base64.b64encode(b).decode()
 for dep in header(b)[2]:collect(dep)
for h in roots:collect(h)
fixture=dict(roots=roots,source_sha=bytes(m['Hashes'][-1]).hex(),size=m['Sizes'][-1],objects=objects)
(out/'clr-replay.json.gz').write_bytes(gzip.compress(json.dumps(fixture,sort_keys=True,separators=(',',':')).encode(),mtime=0))
print('verified',len(normal),'raw families,',len(components),'structural families;',len(objects),'pinned CLR closure objects')
for x in normal:print(x['Family'],f"{x['FineCDC']/x['CDC']:.3f}",f"{x['Authoritative']/x['CDC']:.3f}",x['Deltas'],x['Multi'])
