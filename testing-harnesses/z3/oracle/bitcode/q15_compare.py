"""Compare independently emitted native block/record counts and ID-churn diagnostics.
Usage: CACHE FRAME_CLI. LLVM IR function names annotate diagnostics only.
"""
import collections,json,pathlib,re,subprocess,sys
cache=pathlib.Path(sys.argv[1]);frame=sys.argv[2];rows=json.loads((cache/'q15-corpus/inputs.json').read_text());out=[];previous={}
for r in rows:
 p=pathlib.Path(r['path']);plan=json.loads(subprocess.check_output([frame,str(p)],timeout=60));text=p.with_suffix('.dump').read_text();native={};instances={}
 for section in re.split(r'  Block ID #',text)[1:]:
  bid=section.split()[0].split(':')[0];native[bid]=int(re.search(r'(?:Num Records|Tot/Avg Records): (\d+)',section)[1]);instances[bid]=int(re.search(r'Num Instances: (\d+)',section)[1])
 assert plan['Records']==native and plan['Blocks']==instances,(r['path'],plan['Records'],native)
 names=re.findall(r'^define .*?@(?:"([^"]+)"|([-\w.$]+))\(',p.with_suffix('.ll').read_text(),re.M);names=[a or b for a,b in names];assert len(names)==len(plan['Functions']) and len(set(names))==len(names)
 functions=dict(zip(names,plan['Functions']));old=previous.get(r['family'],{});common=set(old)&set(functions);counts=dict(common=len(common),byte_same=0,operands_same=0,shape_same=0,encoding_only_changes=0)
 for n in common:
  a,b=old[n],functions[n]
  for field,key in [('ByteSHA','byte_same'),('OperandSHA','operands_same'),('ShapeSHA','shape_same')]:counts[key]+=a[field]==b[field]
  counts['encoding_only_changes']+=a['OperandSHA']==b['OperandSHA'] and a['ByteSHA']!=b['ByteSHA']
 previous[r['family']]=functions
 out.append(dict(family=r['family'],stage=r['stage'],path=r['path'],blocks=sum(instances.values()),records=sum(native.values()),abbrevs=plan['Abbrevs'],functions=len(functions),churn=counts,function_fingerprints=functions))
(cache/'q15-corpus/oracle-comparison.json').write_text(json.dumps(out,indent=1));print(json.dumps(dict(inputs=len(out),native_counts_equal=True,blocks=sum(r['blocks'] for r in out),records=sum(r['records'] for r in out))))
