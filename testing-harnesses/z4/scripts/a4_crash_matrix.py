"""Self-SIGKILL at actual publication hooks; real restart/deep proof."""
import json,pathlib,subprocess,sys
w,go=map(pathlib.Path,sys.argv[1:3]);d=w;s=(d/'probe.go').read_text();assert s.count('func main() {')==1;s=s.replace('func main() {','func main() { testHook=func(point string){if point==os.Getenv("Z4_KILL_HOOK"){p,_:=os.FindProcess(os.Getpid());p.Kill();select{}}};',1);(d/'kill-probe.go').write_text(s)
subprocess.run([str(go),'build','-o',str(d/'kill-probe'),str(d/'production.go'),str(d/'kill-probe.go')],check=True)
import os
rows=[]
for kind,fixture in [('clr','/workspace/work/q6-public/clr/13.0.4.dll'),('dex','/workspace/work/q6-public/fdroid-apk/2.0.0.dex')]:
 for hook,floor,roots in [('rep-root-before-format',1,0),('rep-root-after-format',9,0),('rep-root-before-rename',9,0),('rep-root-after-rename',9,1),('rep-root-published',9,1)]:
  store=w/('crash-'+kind+'-'+hook);env=dict(os.environ,Z4_KILL_HOOK=hook);r=subprocess.run([str(d/'kill-probe'),kind+'@'+str(store),fixture],env=env,capture_output=True,text=True);assert r.returncode==-9,(hook,r.returncode,r.stderr)
  fmt=json.loads((store/'FORMAT.json').read_text());assert fmt['store_format_version']==floor,fmt
  v=subprocess.run([str(w/'zeros3'),'verify','-deep','-json','-store',str(store)],capture_output=True,text=True);assert v.returncode==0,v.stderr;vr=json.loads(v.stdout);assert vr['representation_roots']==roots,vr
  old=subprocess.run(['/workspace/work/z4-a3-final/zeros3','verify','-deep','-store',str(store)],capture_output=True,text=True);assert (old.returncode==0)==(floor==1),(hook,old.stderr)
  rows.append(dict(kind=kind,hook=hook,kill_exit=r.returncode,floor=floor,roots=roots,new_verify=vr,old_exit=old.returncode,old_stderr=old.stderr))
(w/'crashes.json').write_text(json.dumps(rows,indent=1)+'\n');print('ten real SIGKILL/restart floor-nine boundaries passed')
