"""Three serial fresh processes, all preselected cases; run without other loads."""
import json,os,pathlib,subprocess,sys,time
root,w=map(pathlib.Path,sys.argv[1:]);inputs=json.loads((root/'testing-harnesses/z4/testdata/a2-model/preselected-phase-inputs.json').read_text())
for trial in range(3):
 d=w/f'phases-{trial}';d.mkdir();start=time.monotonic()
 with (d/'results.jsonl').open('w') as out,(d/'stderr.log').open('w') as err:
  p=subprocess.Popen([str(w/'model/probe'),'model',*[r['path'] for r in inputs]],stdout=out,stderr=err);_,status,usage=os.wait4(p.pid,0);code=os.waitstatus_to_exitcode(status);p.returncode=code
 rows=[json.loads(s) for s in (d/'results.jsonl').read_text().splitlines()];assert len(rows)==len(inputs) and all(r.get('exact') for r in rows),rows
 resource=dict(trial=trial,exit=code,seconds=time.monotonic()-start,maxrss_kib=usage.ru_maxrss,user_cpu=usage.ru_utime,system_cpu=usage.ru_stime,inputs=inputs)
 (d/'resource.json').write_text(json.dumps(resource,indent=1)+'\n');print(json.dumps(resource),flush=True);assert code==0
