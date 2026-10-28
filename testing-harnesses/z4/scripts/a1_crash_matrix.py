"""Self-SIGKILL at actual publication hooks; real restart/deep proof."""
import json,pathlib,subprocess,sys
w,go=map(pathlib.Path,sys.argv[1:3]);d=w/'model';s=(d/'probe.go').read_text();assert s.count('func main() {')==1;s=s.replace('func main() {','func main() { testHook=func(point string){if point==os.Getenv("Z4_KILL_HOOK"){p,_:=os.FindProcess(os.Getpid());p.Kill();select{}}};',1);(d/'kill-probe.go').write_text(s)
subprocess.run([str(go),'build','-o',str(d/'kill-probe'),str(d/'production.go'),str(d/'kill-probe.go')],check=True)
import os
rows=[];fixture='/workspace/work/q9-report-fix/testing-harnesses/testdata/i2-draft/model.safetensors'
for hook,floor,roots in [(('rep-root-before-format' if '--production' in sys.argv else 'z4-before-capability'),1,0),('rep-root-after-format',7,0),('rep-root-before-rename',7,0),('rep-root-after-rename',7,1),('rep-root-published',7,1)]:
 store=w/('crash-v2-'+hook);env=dict(os.environ,Z4_KILL_HOOK=hook);r=subprocess.run([str(d/'kill-probe'),'model@'+str(store),fixture],env=env,capture_output=True,text=True);assert r.returncode==-9,(hook,r.returncode,r.stderr)
 fmt=json.loads((store/'FORMAT.json').read_text());assert fmt['store_format_version']==floor,fmt
 v=subprocess.run([str(d/'zeros3'),'verify','-deep','-json','-store',str(store)],capture_output=True,text=True);assert v.returncode==0,v.stderr;vr=json.loads(v.stdout);assert vr['representation_roots']==roots,vr
 old=subprocess.run(['/workspace/work/z4-a0/i1','verify','-deep','-store',str(store)],capture_output=True,text=True);assert (old.returncode==0)==(floor==1),(hook,old.stderr)
 rows.append(dict(hook=hook,kill_exit=r.returncode,floor=floor,roots=roots,new_verify=vr,old_exit=old.returncode,old_stderr=old.stderr))
(w/'crashes.json').write_text(json.dumps(rows,indent=1)+'\n');print('five SIGKILL/restart boundaries passed')
