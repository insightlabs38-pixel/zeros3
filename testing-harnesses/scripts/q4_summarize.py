"""Summarize saved driver/oracle evidence: q4_summarize.py WORK OUTPUT_JSON."""
from pathlib import Path
from collections import Counter
import glob,json,sys
root=Path(sys.argv[1]);out={'histories':{},'portable_semantic_churn':{},'dwarf_attribute_churn':{}}
for path in sorted(root.glob('*.jsonl')):
 if not path.name.startswith(('controlled-','rows-','target-','real-')):continue
 rows=[json.loads(x) for x in path.read_text().splitlines()]
 if not rows:continue
 # Never turn a bad canonical measurement into a reported PASS.
 if any(not x['cdc']['exact'] or not x['candidate']['exact'] or x['cdc'].get('err') or x['candidate'].get('err') for x in rows):raise RuntimeError('nonexact history '+path.name)
 a=sum(x['cdc']['pack'] for x in rows);b=sum(x['candidate']['pack'] for x in rows)
 out['histories'][path.name]={'target':rows[0]['target'],'candidate_over_cdc':b/a,'cdc_total':a,'candidate_total':b,'revisions':[{'bytes':x['bytes'],'sha256':x['sha256'],'cdc':x['cdc']['pack'],'candidate':x['candidate']['pack'],'refs':x['candidate']['refs'],'meta':x['candidate']['meta'],'elapsed_ms':x['elapsed_ms'],'fallback':x['candidate']['fallback']} for x in rows]}
base=json.loads((root/'oracle-0.json').read_text())
for rev in range(1,5):
 b=json.loads((root/f'oracle-{rev}.json').read_text());shared=base['methods'].keys()&b['methods'].keys();counts=Counter()
 for k in shared:
  for attr in base['methods'][k]:
   if base['methods'][k][attr]!=b['methods'][k][attr]:counts[attr]+=1
 driver=json.loads((root/'controlled-portable.pdb.jsonl').read_text().splitlines()[rev])
 if b['rows']!=driver['rows']:raise RuntimeError('independent PDB row oracle mismatch')
 out['portable_semantic_churn'][str(rev)]={'stable_method_keys':len(shared),'changed_fields':dict(counts),'rows':b['rows']}
for path in root.glob('snapshot-*.jsonl'):
 records=[json.loads(x)['snapshot']['Named'] for x in path.read_text().splitlines()];arr=[]
 for b in records[1:]:
  a=records[0];counts=Counter();shared=a.keys()&b.keys()
  for k in shared:
   for attr in a[k].keys()|b[k].keys():
    if a[k].get(attr)!=b[k].get(attr):counts[attr]+=1
  arr.append({'stable_named_DIEs':len(shared),'changed_attribute_counts':dict(counts)})
 out['dwarf_attribute_churn'][path.name]=arr
out['public_process']=json.loads((root/'public-memory.json').read_text())
Path(sys.argv[2]).write_text(json.dumps(out,indent=2)+'\n')
