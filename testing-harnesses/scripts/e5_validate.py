"""Close CRAM exact byte proofs, native spans/negative semantics and reference costs."""
import json,pathlib,subprocess,sys,tempfile
work=pathlib.Path(sys.argv[1]);rows=[json.loads(s) for s in (work/'results.jsonl').read_text().splitlines()];inputs=json.loads((work/'inputs.json').read_text());assert len(inputs)==25 and len(rows)==138
assert len({r['sha256'] for r in inputs})==25
for r in rows:
 if r['OuterExact']:assert r['InteriorProofs']==[r['SHA']] and len(r['Ranges'] or [])==3 and r['Additive']>=r['Cumulative']
 else:assert r['InteriorProofs'] and r['Additive'] is None and not r['Ranges']

for r in inputs:
 p=pathlib.Path(r['path']);assert p.stat().st_size==r['bytes']<=128<<20
 if r['family'].startswith('cram'):assert r['native_semantic_signature']=='PASS' and r['reads']>=32000
negative=[r for r in inputs if r.get('native_semantic_signature')=='KNOWN_JAVA_AUX_TYPE_MISMATCH'];assert len(negative)==1 and negative[0]['stage']=='auxf#values_java.cram';assert len(negative[0]['known_semantic_mismatch'])==7
assert sum(len(r.get('native_blocks',[])) for r in inputs)==2628
checks=[]
with tempfile.TemporaryDirectory() as td:
 p=pathlib.Path(td)/'input.json'
 for name,changes,reason in [('wrong-source',{'sha256':'0'*64},'source hash'),('overlap',{'resource_spans':[[32,20],[33,20]]},'native span bounds')]:
  r=dict(inputs[0],**changes);p.write_text(json.dumps([r]));out=subprocess.run([str(work/'frontier'),str(p)],capture_output=True,text=True,timeout=15);assert out.returncode and reason in out.stderr;checks.append(name)
summary=dict(inputs=len(inputs),unique_sources=25,rows=len(rows),serialized_proofs=sum(len(r['InteriorProofs']) for r in rows),cold_ranges=sum(len(r['Ranges'] or []) for r in rows),negative_checks=checks,finals=[])
for family in dict.fromkeys(r['Family'] for r in rows):
 for v in dict.fromkeys(r['Variant'] for r in rows if r['Family']==family and r['OuterExact']):
  r=next(r for r in reversed(rows) if (r['Family'],r['Variant'])==(family,v));summary['finals'].append(dict(family=family,variant=v,bytes=r['Cumulative'],additive=r['Additive'],max_range_amplification=max(s['ReadBytes']/s['Length'] for o in rows if (o['Family'],o['Variant'])==(family,v) for s in (o['Ranges'] or []))))
(work/'summary.json').write_text(json.dumps(summary,indent=1));print(json.dumps(summary))
