"""Serial production-path phase samples; cached native corpus preselected in A0.
ROOT CACHE. Three fresh-store samples per non-GiB cohort; one existing GiB sample.
The ordinary authoritative CDC ingest+namespace commit precedes optional encoding.
"""
import hashlib,json,os,pathlib,subprocess,sys,time
root,cache=map(pathlib.Path,sys.argv[1:3]);work=cache/'z4-a0';rows=json.loads((root/'testing-harnesses/z4/testdata/a0-audit/preselected-inputs.json').read_text());groups={}
for r in rows:groups.setdefault(r['kind']+('-gib' if r['bytes']==1<<30 else ''),[]).append(r)
for group,inputs in groups.items():
 kind=inputs[0]['kind'];binary=work/('i2' if kind in ['model','dex','clr'] else 'i3')/'probe'
 for trial in range(1 if group.endswith('-gib') else 3):
  out=work/'phase-v2'/f'{group}-{trial}';out.mkdir(parents=True,exist_ok=True)
  if (out/'resource.json').exists():
   prior=json.loads((out/'resource.json').read_text());assert prior['exit']==0 or prior.get('rows_verified_complete'),'preserve failed run and use a new run directory';continue
  start=time.monotonic()
  with (out/'results.jsonl').open('w') as stdout,(out/'stderr.log').open('w') as stderr:
   p=subprocess.Popen([str(binary),kind,*[r['path'] for r in inputs]],stdout=stdout,stderr=stderr);_,status,usage=os.wait4(p.pid,0);code=os.waitstatus_to_exitcode(status);p.returncode=code
  resource=dict(group=group,trial=trial,exit=code,seconds=time.monotonic()-start,maxrss_kib=usage.ru_maxrss,user_cpu=usage.ru_utime,system_cpu=usage.ru_stime,inputs=inputs,scope='fresh child; ordinary CDC setup plus separately timed optional encode/CAS, flush, proof, reachability, byte-equal cold decoder and root publication; 10ms memory/RSS sampling; no producer/compiler')
  (out/'resource.json').write_text(json.dumps(resource,indent=1));print(json.dumps(resource),flush=True);assert code==0
