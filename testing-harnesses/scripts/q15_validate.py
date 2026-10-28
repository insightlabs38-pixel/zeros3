"""Close bitcode exact closure, restoration, cost and rejection evidence."""
import json,pathlib,subprocess,sys,tempfile
work=pathlib.Path(sys.argv[1]);rows=[json.loads(s) for s in (work/'results.jsonl').read_text().splitlines()];inputs=json.loads((work/'inputs.json').read_text());assert len(inputs)==65 and len(rows)==325
assert len({r['sha256'] for r in inputs})==56
for r in rows:
 assert r['OuterExact'] and r['InteriorProofs']==[r['SHA']] and len(r['Ranges'])==3 and r['Additive']>=r['Cumulative']
 if r['File']=='restore':assert r['Incremental']==0
checks=[]
with tempfile.TemporaryDirectory() as td:
 p=pathlib.Path(td)/'input.json'
 for name,changes,reason in [('wrong-source',{'sha256':'0'*64},'source hash'),('missing-file',{'path':str(pathlib.Path(td)/'absent.bc')},'no such file')]:
  r=dict(inputs[0],**changes);p.write_text(json.dumps([r]));out=subprocess.run([str(work/'frontier'),str(p)],capture_output=True,text=True,timeout=15);assert out.returncode and reason in out.stderr;checks.append(name)
summary=dict(inputs=len(inputs),unique_sources=56,rows=len(rows),serialized_proofs=len(rows),cold_ranges=sum(len(r['Ranges']) for r in rows),negative_checks=checks,finals=[])
for family in dict.fromkeys(r['Family'] for r in rows):
 for v in dict.fromkeys(r['Variant'] for r in rows if r['Family']==family):
  r=next(r for r in reversed(rows) if (r['Family'],r['Variant'])==(family,v));summary['finals'].append(dict(family=family,variant=v,bytes=r['Cumulative'],additive=r['Additive'],max_range_amplification=max(s['ReadBytes']/s['Length'] for o in rows if (o['Family'],o['Variant'])==(family,v) for s in o['Ranges'])))
(work/'summary.json').write_text(json.dumps(summary,indent=1));print(json.dumps(summary))
