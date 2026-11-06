"""Run serial native/source-density controls. No test/build/producer may overlap.
ROOT WORK PROBE INPUTS_JSON REPEATS [--monitor-tmp]. All raw results retained.
"""
import hashlib,json,os,pathlib,shutil,subprocess,sys,time
root,w,probe,inputs=map(pathlib.Path,sys.argv[1:5]);repeats=int(sys.argv[5]);monitor='--monitor-tmp' in sys.argv
w.mkdir(parents=True,exist_ok=True);rows=json.loads(inputs.read_text())
for i,row in enumerate(rows):
 assert hashlib.sha256(pathlib.Path(row['path']).read_bytes()).hexdigest()==row['sha256']
 for trial in range(repeats):
  d=w/f'case-{i}-{trial}';d.mkdir();store=d/'store';start=time.monotonic();peak=0;files=0;managed_peak=0;cas_peak=0
  with (d/'results.jsonl').open('w') as out,(d/'stderr.log').open('w') as err:
   mode=row['kind']+'@'+str(store) if monitor else row['kind']
   p=subprocess.Popen([str(probe),mode,row['path']],stdout=out,stderr=err)
   if monitor:
    while True:
     pid,status,usage=os.wait4(p.pid,os.WNOHANG)
     if pid:break
     paths=list((store/'tmp').glob('*'));sizes=[];managed=0;cas=0
     for f in paths:
      try:
       size=f.stat().st_size;sizes.append(size)
       if f.name.startswith("zeros3-"):managed+=size
       else:cas+=size
      except FileNotFoundError:pass
     peak=max(peak,sum(sizes));files=max(files,len(sizes));managed_peak=max(managed_peak,managed);cas_peak=max(cas_peak,cas);time.sleep(.01)
   else:_,status,usage=os.wait4(p.pid,0)
   p.returncode=os.waitstatus_to_exitcode(status)
  result=dict(input=row,trial=trial,exit=p.returncode,seconds=time.monotonic()-start,maxrss_kib=usage.ru_maxrss,monitor_tmp=monitor,tmp_peak_bytes=peak,tmp_peak_files=files,managed_tmp_peak_bytes=managed_peak,cas_tmp_peak_bytes=cas_peak)
  (d/'resource.json').write_text(json.dumps(result,indent=1)+'\n');print(json.dumps(result),flush=True)
  assert p.returncode==0,result
  r=json.loads((d/'results.jsonl').read_text());assert r.get('exact') and r['sha256']==row['sha256'] and r['bytes']==row['bytes'] and r['store_floor']==9,r
  if monitor:
   # Only managed spool names; persistent CAS staging is a distinct category.
   leftovers=[p.name for p in (store/'tmp').glob('zeros3-*')];assert not leftovers,leftovers
   # This exact verified run is reproducible; retain results rather than duplicate stores.
   shutil.rmtree(store)
