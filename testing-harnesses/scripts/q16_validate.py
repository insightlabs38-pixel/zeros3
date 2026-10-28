"""Close broad-tail byte proofs, native provenance and ONNX candidate prices."""
import json,pathlib,subprocess,sys,tempfile
work=pathlib.Path(sys.argv[1]);rows=[json.loads(s) for s in (work/'results.jsonl').read_text().splitlines()];inputs=json.loads((work/'inputs.json').read_text());assert len(inputs)==44 and len(rows)==214
assert len({r['sha256'] for r in inputs})==41
for r in rows:
 if r['OuterExact']:assert r['InteriorProofs']==[r['SHA']] and len(r['Ranges'] or [])==3 and r['Additive']>=r['Cumulative']
 else:assert r['InteriorProofs'] and r['Additive'] is None and not r['Ranges']
 if r['Family']=='onnx-squeezenet' and r['File']=='restore':assert r['Incremental']==0
for r in inputs:
 p=pathlib.Path(r['path']);assert p.stat().st_size==r['bytes']<=128<<20
 if r['family'].startswith('wal-'):assert r['native_restore'] and r['frames']>0 and r['logical_sha256'] and r['base_sha256']
checks=[]
with tempfile.TemporaryDirectory() as td:
 p=pathlib.Path(td)/'input.json'
 for name,changes,reason in [('wrong-source',{'sha256':'0'*64},'source hash'),('overlap',{'chunk_spans':[[32,20],[33,20]]},'native span bounds')]:
  r=dict(inputs[0],**changes);p.write_text(json.dumps([r]));out=subprocess.run([str(work/'frontier'),str(p)],capture_output=True,text=True,timeout=15);assert out.returncode and reason in out.stderr;checks.append(name)
summary=dict(inputs=len(inputs),unique_sources=41,rows=len(rows),serialized_proofs=sum(len(r['InteriorProofs']) for r in rows),cold_ranges=sum(len(r['Ranges'] or []) for r in rows),negative_checks=checks,finals=[])
for family in dict.fromkeys(r['Family'] for r in rows):
 for v in dict.fromkeys(r['Variant'] for r in rows if r['Family']==family and r['OuterExact']):
  r=next(r for r in reversed(rows) if (r['Family'],r['Variant'])==(family,v));summary['finals'].append(dict(family=family,variant=v,bytes=r['Cumulative'],additive=r['Additive'],max_range_amplification=max(s['ReadBytes']/s['Length'] for o in rows if (o['Family'],o['Variant'])==(family,v) for s in (o['Ranges'] or []))))
(work/'summary.json').write_text(json.dumps(summary,indent=1));print(json.dumps(summary))
