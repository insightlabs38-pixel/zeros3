"""Serial fresh-store/process phase samples. ROOT WORK. No concurrent workloads.
Three uninstrumented samples/input except one GiB; one stage-profiled sample/input.
"""
import json,os,pathlib,subprocess,sys,time
root,w=map(pathlib.Path,sys.argv[1:3]);inputs=json.loads((root/'testing-harnesses/z4/testdata/a3-structured'/('preselected-final-inputs.json' if '--final' in sys.argv else 'preselected-inputs.json')).read_text())
for mode in ['plain','profiled']:
 for i,r in enumerate(inputs):
  for trial in range(1 if mode=='profiled' or r['bytes']==1<<30 else 3):
   d=w/f'phase-{mode}-{i}-{trial}';d.mkdir();start=time.monotonic()
   with (d/'results.jsonl').open('w') as out,(d/'stderr.log').open('w') as err:
    p=subprocess.Popen([str(w/mode/'probe'),r['kind'],r['path']],stdout=out,stderr=err);_,status,usage=os.wait4(p.pid,0);code=os.waitstatus_to_exitcode(status);p.returncode=code
   resource=dict(mode=mode,input=r,trial=trial,exit=code,seconds=time.monotonic()-start,maxrss_kib=usage.ru_maxrss,user_cpu=usage.ru_utime,system_cpu=usage.ru_stime)
   (d/'resource.json').write_text(json.dumps(resource,indent=1)+'\n');print(json.dumps(resource),flush=True);assert code==0
   rows=[json.loads(s) for s in (d/'results.jsonl').read_text().splitlines()];assert len(rows)==1 and rows[0].get('exact'),rows;assert rows[0]['sha256']==r['sha256'] and rows[0]['bytes']==r['bytes'],rows[0]
   if mode=='profiled':
    row=rows[0];stages=row['analysis_stages_ns'];elapsed=row['phases']['encode_with_cas']['NS'];assert stages and min(stages.values())>=0 and sum(stages.values())<=elapsed,(stages,elapsed)
