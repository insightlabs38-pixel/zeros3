"""Build the research CLI and measure the complete bounded shootout.
Usage: q16_measure.py ROOT CACHE WORK GO; native producer outputs are CACHE/q16-corpus. Compilation is excluded from child RSS/time.
"""
import hashlib,json,os,pathlib,subprocess,sys,time
root,cache,work=map(pathlib.Path,sys.argv[1:4]);compiler=sys.argv[4];work.mkdir(parents=True,exist_ok=True)
inputs=json.loads((cache/'q16-corpus/inputs.json').read_text())
(work/'inputs.json').write_text(json.dumps(inputs,indent=1))
subprocess.run([compiler,'-C',str(root/'testing-harnesses/z3'),'build','-o',str(work/'frontier'),'./cmd/z3tailfrontier'],check=True)
p=subprocess.Popen([str(work/'frontier'),str(work/'inputs.json')],stdout=open(work/'results.jsonl','w'),stderr=open(work/'run.log','w'));start=time.monotonic();_,status,u=os.wait4(p.pid,0);code=os.waitstatus_to_exitcode(status);p.returncode=code
files=[*sorted((root/'testing-harnesses/z3/onnxz3').glob('*.go')),root/'testing-harnesses/z3/cmd/z3tailfrontier/main.go',*sorted((root/'testing-harnesses/z3/oracle/tail').glob('*'))]
meta=dict(exit=code,seconds=time.monotonic()-start,user_cpu=u.ru_utime,system_cpu=u.ru_stime,maxrss_kib=u.ru_maxrss,inputs=len(inputs),scope='Broad-tail native-oracle boundary and logical-component characterization, including CDC baselines, CAS, serialized full proof/reachability, packed costs and fresh-source ranges; not a streaming production decoder',sources={str(f.relative_to(root)):hashlib.sha256(f.read_bytes()).hexdigest() for f in files})
(work/'resource.json').write_text(json.dumps(meta,indent=1));print(json.dumps(meta));assert code==0
