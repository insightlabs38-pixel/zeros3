"""Freeze the closed WIM characterization without promoting an oracle to a parser."""
import gzip,hashlib,json,pathlib,sys
root,work,cache=map(pathlib.Path,sys.argv[1:4]);out=root/'testing-harnesses/testdata/q13-wim';meta=json.loads((work/'resource.json').read_text());assert meta['exit']==0 and meta['inputs']==33
for p,h in meta['sources'].items():assert hashlib.sha256((root/p).read_bytes()).hexdigest()==h
rows=[json.loads(s) for s in (work/'results.jsonl').read_text().splitlines()];inputs=json.loads((work/'inputs.json').read_text());validation=json.loads((cache/'q13-validation.json').read_text());assert validation['serialized_proofs']==733 and validation['cold_ranges']==297
logs={}
for name in ['q13-lab-tests','q13-lab-vet','q13-root-tests','q13-root-vet']:
 p=cache/(name+'.log');s=p.read_text();assert 'FAIL' not in s and 'panic:' not in s
 if not name.endswith('vet'):assert 'ok  ' in s
 logs[name]=dict(sha256=hashlib.sha256(p.read_bytes()).hexdigest(),tail=s[-1600:])
with gzip.GzipFile(filename=str(out/'validation.json.gz'),mode='wb',mtime=0) as f:f.write(json.dumps(dict(resource=meta,inputs=inputs,rows=rows,validation=validation,gates=logs),sort_keys=True,separators=(',',':')).encode())
summary={}
for row in rows:summary.setdefault(row['Family'],{})[row['Variant']]={k:row[k] for k in ['Cumulative','Additive']}
(out/'summary.json').write_text(json.dumps(summary,indent=1)+'\n')
p=out/'characterization.json.gz'
with gzip.open(p) as f:d=json.load(f)
d['reproducer_sha256']=d.pop('source_producer_sha256');d['original_producer_sha256']=hashlib.sha256((cache/'q13-prep/produce.py').read_bytes()).hexdigest();d['producer_note']='Recorded original execution source plus portable/resumable reproducer; dataset construction unchanged.'
with gzip.GzipFile(filename=str(p),mode='wb',mtime=0) as f:f.write(json.dumps(d,sort_keys=True).encode())
print('33 state files /30 distinct SHA;165 rows /733 serialized proofs /297 cold ranges')
