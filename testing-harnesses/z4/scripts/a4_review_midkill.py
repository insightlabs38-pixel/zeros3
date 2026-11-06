"""Kill during actual managed spooling; prove CDC/old-floor safety and GC cleanup.
WORK CANDIDATE PROBE. Fresh directories required; failure evidence is never removed.
"""
import json,os,pathlib,subprocess,sys,time
w,binary,probe=map(pathlib.Path,sys.argv[1:4]);rows=[]
for kind,path in [('clr','/workspace/work/q4/dotnet/sdk/8.0.427/Roslyn/bincore/Microsoft.CodeAnalysis.CSharp.dll'),('dex','/workspace/work/q6-public/fdroid-apk/2.0.0.dex')]:
 d=w/('midkill-'+kind);d.mkdir();store=d/'store';observed=[]
 with (d/'stdout.log').open('w') as out,(d/'stderr.log').open('w') as err:
  p=subprocess.Popen([str(probe),kind+'@'+str(store),path],stdout=out,stderr=err);deadline=time.monotonic()+30
  while p.poll() is None and time.monotonic()<deadline:
   observed=[]
   for f in (store/'tmp').glob('zeros3-*'):
    try:observed.append(dict(name=f.name,bytes=f.stat().st_size))
    except FileNotFoundError:pass
   if any(f['bytes']>0 for f in observed):p.kill();break
   time.sleep(.001)
  if not observed or not any(f['bytes']>0 for f in observed):
   p.kill();p.wait();raise AssertionError('spooling kill boundary not reached: '+kind)
  code=p.wait();assert code==-9,code
 fmt=json.loads((store/'FORMAT.json').read_text());assert fmt['store_format_version']==1,fmt
 results={}
 for label,cmd in [('new_verify',[str(binary),'verify','-deep','-json','-store',str(store)]),('old_verify',['/workspace/work/z4-a3-final/zeros3','verify','-deep','-store',str(store)]),('gc',[str(binary),'gc','-apply','-store',str(store)]),('post_gc',[str(binary),'verify','-deep','-json','-store',str(store)])]:
  r=subprocess.run(cmd,capture_output=True,text=True);results[label]=dict(exit=r.returncode,stdout=r.stdout,stderr=r.stderr);assert r.returncode==0,results[label]
 assert not list((store/'tmp').glob('zeros3-*')),'managed staging leak after offline GC'
 row=dict(kind=kind,exit=code,observed=observed,floor=1,results=results);rows.append(row);print(json.dumps(row),flush=True)
(w/'midkill-results.json').write_text(json.dumps(rows,indent=1)+'\n')
