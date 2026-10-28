"""Freeze source-addressed Q12 results only after the declared gates pass."""
import collections,gzip,hashlib,json,pathlib,sys
root,work,cache=map(pathlib.Path,sys.argv[1:4]);out=root/'testing-harnesses/testdata/q12-scientific'
meta=json.loads((work/'resource.json').read_text());assert meta['exit']==0 and meta['inputs']==68
for p,h in meta['sources'].items():assert hashlib.sha256((root/p).read_bytes()).hexdigest()==h,p
rows=[json.loads(s) for s in (work/'results.jsonl').read_text().splitlines()];assert len(rows)==354
assert sum(len(r['InteriorProofs']) for r in rows)==654
assert sum(len(r['Ranges'] or []) for r in rows)==762
for r in rows:
 assert r['InteriorProofs']
 if r['OuterExact']:
  assert r['InteriorProofs']==[r['SHA']] and len(r['Ranges'])==3
  assert r['Additive']>=r['Cumulative']
 else:assert len(r['InteriorProofs'])==4 and r['Additive'] is None
refs=json.loads((cache/'q12-reference-oracle.json').read_text());assert len(refs['references'])==18 and all(r['read'] for r in refs['references'])
gates={}
for n in ['q12-closed-tests','q12-closed-vet','q12-closed-race','q12-zstd-race','q12-root-tests','q12-root-vet','q12-fuzz','q12-auth-range','q12-admission']:
 p=cache/(n+'.log');s=p.read_text();assert 'FAIL' not in s and 'panic:' not in s,n
 if not n.endswith('vet'):assert 'ok  ' in s,n
 gates[n]=dict(sha256=hashlib.sha256(p.read_bytes()).hexdigest(),tail=s[-2200:])
p=cache/'q12-race.log';s=p.read_text();assert 'test timed out after 10m0s' in s
broad=dict(scope='broader unchanged legacy kinds suite; not the required changed-surface race gate',outcome='default600s timeout without assertion failure',sha256=hashlib.sha256(p.read_bytes()).hexdigest(),head=s[:700])
result=dict(resource=meta,rows=rows,native_reference_oracle=refs,gates=gates,broad_legacy_race=broad)
with gzip.GzipFile(filename=str(out/'validation.json.gz'),mode='wb',mtime=0) as f:f.write(json.dumps(result,sort_keys=True,separators=(',',':')).encode())
summary={}
for r in rows:
 if not r['Class'].startswith('REFERENCE'):summary.setdefault(r['Family'],{})[r['Variant']]={k:r[k] for k in ['Cumulative','Additive','Fallback']}
(out/'summary.json').write_text(json.dumps(summary,indent=1)+'\n')
print('354 rows,654 serialized proofs,762 authenticated cold ranges; gates complete')
print('resource',meta['seconds'],meta['maxrss_kib'])
