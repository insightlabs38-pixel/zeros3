"""Replay all immutable draft native cases, retaining every accepted/fallback case.
Timings are diagnostic only; concurrent test loads invalidate latency claims.
"""
import json,pathlib,subprocess,sys
root,w,go=map(pathlib.Path,sys.argv[1:]);d=w/'native-probe';d.mkdir(exist_ok=True);s=(root/'zeros3.go').read_text().replace('func main() {','func privateProductMain() {',1);(d/'production.go').write_text(s);(d/'probe.go').write_bytes((root/'testing-harnesses/z4/probes/a3_native.go.txt').read_bytes());subprocess.run([str(go),'build','-o',str(d/'probe'),str(d/'production.go'),str(d/'probe.go')],check=True)
inputs=json.loads((root/'testing-harnesses/z4/testdata/a3-structured/native-regression-inputs.json').read_text());summary=[]
for kind in ['sst','parquet']:
 rows=[r for r in inputs if r['kind']==kind]
 with (w/('native-'+kind+'.jsonl')).open('w') as out,(w/('native-'+kind+'.log')).open('w') as err:res=subprocess.run([str(d/'probe'),kind,*[r['path'] for r in rows]],stdout=out,stderr=err)
 got=[json.loads(s) for s in (w/('native-'+kind+'.jsonl')).read_text().splitlines()];assert res.returncode==0 and len(got)==len(rows),(kind,res.returncode,len(got))
 for want,actual in zip(rows,got):
  assert actual['SHA']==want['sha256'] and actual['Bytes']==want['bytes'],(want,actual)
  assert actual['Exact']==want['expected_exact'],(want,actual)
  if actual['Exact']:assert actual['Root']==want['expected_root'],(want,actual)
 summary.append(dict(kind=kind,cases=len(rows),exact=sum(r['Exact'] for r in got),fallback=sum(not r['Exact'] for r in got),exact_root_identity_matches=True))
(w/'native-summary.json').write_text(json.dumps(summary,indent=1)+'\n');print(summary)
