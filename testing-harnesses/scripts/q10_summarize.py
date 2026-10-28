"""Assert closed I3 evidence and freeze bounded, source-addressed results."""
import collections,gzip,hashlib,json,pathlib,sys
root=pathlib.Path(sys.argv[1]);work=pathlib.Path(sys.argv[2]);cache=pathlib.Path(sys.argv[3]);out=root/'testing-harnesses/testdata/i3-draft'
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
source=sha(root/'zeros3.go');result={'production_sha256':source,'cohorts':{},'gates':{}}
for kind,count in [('sst',110),('parquet',52)]:
 p=cache/('q10-bounded-'+kind);resource=json.loads((p/'resource.json').read_text());assert resource['exit']==0 and resource['production_sha256']==source
 rows=[json.loads(s) for s in (p/'results.jsonl').read_text().splitlines()];assert len(rows)==count
 for row in rows:
  assert row['Exact'] or row['Fallback']
  if row['Exact']:assert not row['Fallback'] and len(row['Ranges'])==3
 result['cohorts'][kind]={'resource':resource,'proofs':rows}
for name,count in [('compat',54),('bundles',24),('kinds',32),('crash',24),('rollback',23)]:
 p=work/(name+'.log');s=p.read_text();assert s.count('PASS:')==count and 'FAIL:' not in s and 'panic:' not in s
 result['gates'][name]={'passes':count,'sha256':sha(p)}
for name in ['final-guard-full','final-guard-race','final-guard-vet','lab-tests','lab-vet','fuzz','prefix-guard','embedded-zstd']:
 p=work/(name+'.log');s=p.read_text();assert 'FAIL' not in s and 'panic:' not in s
 if name not in ['final-guard-vet','lab-vet']:assert 'ok  ' in s
 result['gates'][name]={'sha256':sha(p),'tail':s[-2400:]}
p=cache/'q10-large-bounded';resource=json.loads((p/'resource.json').read_text());rows=[json.loads(s) for s in (p/'results.jsonl').read_text().splitlines()];assert resource['exit']==0 and resource['production_sha256']==source and rows[0]['Exact'] and rows[0]['Bytes']==1<<30
memory={'producer':json.loads((cache/'q10-large/gib.parquet.json').read_text()),'resource':resource,'proof':rows}
(out/'memory.json').write_text(json.dumps(memory,indent=1)+'\n')
with gzip.GzipFile(filename=str(out/'validation.json.gz'),mode='wb',mtime=0) as f:f.write(json.dumps(result,sort_keys=True,separators=(',',':')).encode())
p=out/'provenance.json';d=json.loads(p.read_text());d['production_sha256']=source;d['local_guards']=list(dict.fromkeys(d['local_guards']+['aggregate prefix-expanded SST key scratch','embedded Zstd page-sized parse and replay limits']));p.write_text(json.dumps(d,indent=1)+'\n')
for kind,d in result['cohorts'].items():
 rows=d['proofs'];print(kind,collections.Counter(r['Exact'] for r in rows),'peak_rss_kib',d['resource']['maxrss_kib'],'peak_sampled_heap_growth',max(r['HeapGrowth'] for r in rows),'max_logical_range_amp',max((v['FetchedBytes']/v['Length'] for r in rows for v in (r['Ranges'] or [])),default=0))
print('production',source,'all gates complete')
