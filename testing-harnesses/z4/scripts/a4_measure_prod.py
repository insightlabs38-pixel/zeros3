"""Serial fresh-store/process production samples; reuse A3 phase accounting."""
import json,os,pathlib,subprocess,sys,time
root,w=map(pathlib.Path,sys.argv[1:3]);inputs=json.loads((root/'testing-harnesses/z4/testdata/a4-managed/preselected-inputs.json').read_text())
for i,r in enumerate(inputs):
 for trial in range(3):
  d=w/f'phase-{i}-{trial}';d.mkdir();start=time.monotonic()
  with (d/'results.jsonl').open('w') as out,(d/'stderr.log').open('w') as err:
   p=subprocess.Popen([str(w/'probe'),r['kind'],r['path']],stdout=out,stderr=err);_,status,usage=os.wait4(p.pid,0);code=os.waitstatus_to_exitcode(status);p.returncode=code
  resource=dict(input=r,trial=trial,exit=code,seconds=time.monotonic()-start,maxrss_kib=usage.ru_maxrss,user_cpu=usage.ru_utime,system_cpu=usage.ru_stime)
  (d/'resource.json').write_text(json.dumps(resource,indent=1)+'\n');print(json.dumps(resource),flush=True);assert code==0
  rows=[json.loads(s) for s in (d/'results.jsonl').read_text().splitlines()];assert len(rows)==1 and rows[0].get('exact'),rows
  assert rows[0]['sha256']==r['sha256'] and rows[0]['bytes']==r['bytes'] and rows[0]['store_floor']==9,rows[0]
  assert rows[0]['production_sha256']==json.loads((w/'build-provenance.json').read_text())['production_sha256']
