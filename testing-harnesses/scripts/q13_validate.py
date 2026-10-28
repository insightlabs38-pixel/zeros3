"""Assert WIM characterization closure/cost evidence and bounded harness rejection."""
import hashlib,json,pathlib,subprocess,sys,tempfile
root,cache,work=map(pathlib.Path,sys.argv[1:4]);rows=[json.loads(s) for s in (work/'results.jsonl').read_text().splitlines()];inputs=json.loads((work/'inputs.json').read_text());assert len(inputs)==33 and len(rows)==165
assert len({x['sha256'] for x in inputs})==30
for r in inputs:
 p=pathlib.Path(r['path']);assert p.stat().st_size==r['bytes']<=16<<20;assert hashlib.sha256(p.read_bytes()).hexdigest()==r['sha256'];assert r['native_verified'] and r['header']['integrity_verified'];assert r['header']['xml_images']==r['header']['images']
 assert {f['sha1'] for f in r['source_tree']}<={f['sha1'] for f in r['native_resources']}
for r in rows:
 if r['OuterExact']:assert r['InteriorProofs']==[r['SHA']] and len(r['Ranges'])==3 and r['Additive']>=r['Cumulative']
 else:assert r['InteriorProofs'] and r['Additive'] is None
checks=[]
with tempfile.TemporaryDirectory() as td:
 td=pathlib.Path(td);r=dict(inputs[0]);p=td/'inputs.json';r['sha256']='0'*64;p.write_text(json.dumps([r]));out=subprocess.run([str(work/'frontier'),str(p)],capture_output=True,text=True,timeout=15);assert out.returncode and 'source hash' in out.stderr;checks.append('source hash')
 r=dict(inputs[0]);r['resource_spans']=[[208,10],[209,10]];p.write_text(json.dumps([r]));out=subprocess.run([str(work/'frontier'),str(p)],capture_output=True,text=True,timeout=15);assert out.returncode and 'native span bounds' in out.stderr;checks.append('overlapping native spans')
 # Native header oracle is bounded and deliberately not a candidate decoder.
 source=pathlib.Path(inputs[0]['path']);bad=td/'bad.wim';b=bytearray(source.read_bytes());b[:8]=b'NOTAWIM!';bad.write_bytes(b);r=dict(inputs[0],path=str(bad),sha256=hashlib.sha256(b).hexdigest());(td/'inputs.json').write_text(json.dumps([r]));out=subprocess.run([sys.executable,str(root/'testing-harnesses/z3/oracle/wim/q13_inventory.py'),str(td)],capture_output=True,text=True,timeout=15);assert out.returncode and 'AssertionError' in out.stderr;checks.append('wrong header magic')
print(json.dumps(dict(inputs=33,unique_inputs=30,rows=165,serialized_proofs=sum(len(r['InteriorProofs']) for r in rows),cold_ranges=sum(len(r['Ranges'] or []) for r in rows),negative_checks=checks)))
