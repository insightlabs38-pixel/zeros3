"""A Concat outer header must not conceal a new child kind from publication."""
import json,pathlib,subprocess,sys
w,go=map(pathlib.Path,sys.argv[1:]);d=w/'model';s=(d/'probe.go').read_text();anchor='phases["finish_root"] =';assert s.count(anchor)==1
s=s.replace(anchor,'childID,err:=b.finishRep(root);auditMust(err);root=&repObject{Kind:repKindConcat,Size:st.Size(),Members:[]repMember{{Role:repDerived,Size:st.Size(),SHA:want,Child:childID}}}\n'+anchor)
(d/'nested.go').write_text(s);subprocess.run([str(go),'build','-o',str(d/'nested'),str(d/'production.go'),str(d/'nested.go')],check=True)
store=w/'nested-model';p=subprocess.run([str(d/'nested'),'model@'+str(store),'/workspace/work/q9-report-fix/testing-harnesses/testdata/i2-draft/model.safetensors'],capture_output=True,text=True);assert p.returncode==0,p.stderr;assert json.loads((store/'FORMAT.json').read_text())['store_format_version']==7
v=subprocess.run([str(d/'zeros3'),'verify','-deep','-json','-store',str(store)],capture_output=True,text=True);assert v.returncode==0,v.stderr
# The model-ceiling candidate contains quarantined managed decoder code only for
# this negative publication check. Its publication ceiling must still reject 11.
blocked=w/'blocked-dex';r=subprocess.run([str(d/'probe'),'dex@'+str(blocked),'/workspace/work/q6-public/fdroid-apk/2.0.2.dex'],capture_output=True,text=True);assert r.returncode!=0 and 'required capability floor 9' in r.stderr,r.stderr
fmt=json.loads((blocked/'FORMAT.json').read_text());assert fmt['store_format_version']==1,fmt;assert not list((blocked/'representations/roots').glob('*.root'))
(w/'nested-floor.json').write_text(json.dumps(dict(nested_exact=json.loads(p.stdout),deep=json.loads(v.stdout),blocked_exit=r.returncode,blocked_stderr=r.stderr,blocked_format=fmt),indent=1)+'\n')
