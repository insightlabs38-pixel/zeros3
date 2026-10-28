"""Summarize canonical native measurements: script WORK OUTPUT_JSON."""
from pathlib import Path
import json,sys,gzip
root=Path(sys.argv[1]);out={'families':{},'process':json.loads((root/'process-memory.json').read_text())}
for path in sorted(root.glob('content-*.jsonl')):
 rows=[json.loads(x) for x in path.read_text().splitlines()]
 if len(rows)!=19:raise RuntimeError('incomplete canonical matrix '+path.name)
 baseline=rows[0]['result'];family={'sources':rows[0]['sources'],'representations':{}}
 for row in rows:
  r=row['result']
  if not r['Exact'] or r.get('Err'):raise RuntimeError('nonexact '+path.name)
  for a in r['PerRev']:
   if not a['exact'] or a.get('err'):raise RuntimeError('nonexact revision')
  family['representations'][row['representation']]={'ratio':r['Total']['pack']/baseline['Total']['pack'],'total':r['Total'],'per_revision':r['PerRev'],'elapsed_ms':row['elapsed_ms'],'statistics':row['stats']}
 family['pairwise_sensitivity_only']=rows[0]['pairwise_sensitivity_only'];out['families'][path.name]=family
data=(json.dumps(out,indent=2)+'\n').encode()
path=Path(sys.argv[2]);path.write_bytes(gzip.compress(data,mtime=0) if path.suffix=='.gz' else data)
