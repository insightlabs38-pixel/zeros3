"""Build the research CLI and measure the complete bounded shootout.
Usage: q12_measure.py ROOT CACHE WORK GO; native producer outputs are CACHE/q12-corpus,
pinned references CACHE/q12-refs. Compilation is excluded from child RSS/time.
"""
import hashlib,json,os,pathlib,subprocess,sys,time
root,cache,work=map(pathlib.Path,sys.argv[1:4]);compiler=sys.argv[4];work.mkdir(parents=True,exist_ok=True)
inputs=json.loads((cache/'q12-corpus/inputs.json').read_text())
for r in json.loads((root/'testing-harnesses/testdata/q12-scientific/public.json').read_text()):
 p=cache/'q12-refs'/r['file'];assert p.stat().st_size==r['bytes']<=16<<20;assert hashlib.sha256(p.read_bytes()).hexdigest()==r['sha256']
 inputs.append(dict(path=str(p),family=r['family']+'/'+r['file'],stage=r['file'],class_='REFERENCE_FIXTURE_NOT_HISTORY',sha256=r['sha256'],bytes=r['bytes'],spans=[],oracles=[]))
(work/'inputs.json').write_text(json.dumps(inputs,indent=1))
subprocess.run([compiler,'-C',str(root/'testing-harnesses/z3'),'build','-o',str(work/'frontier'),'./cmd/z3scientific'],check=True)
p=subprocess.Popen([str(work/'frontier'),str(work/'inputs.json')],stdout=open(work/'results.jsonl','w'),stderr=open(work/'run.log','w'));start=time.monotonic();_,status,u=os.wait4(p.pid,0);code=os.waitstatus_to_exitcode(status);p.returncode=code
files=[root/'testing-harnesses/z3/cmd/z3scientific/main.go',root/'testing-harnesses/z3/repz3/kinds/zstd.go',*sorted((root/'testing-harnesses/z3/scientificz3').glob('*.go'))]
meta=dict(exit=code,seconds=time.monotonic()-start,user_cpu=u.ru_utime,system_cpu=u.ru_stime,maxrss_kib=u.ru_maxrss,inputs=len(inputs),scope='entire proof-heavy research shootout including baseline/oracle/candidate generation, CAS, serialized full proof/reachability, packed costs and fresh-source ranges; not a streaming production decoder',sources={str(f.relative_to(root)):hashlib.sha256(f.read_bytes()).hexdigest() for f in files})
(work/'resource.json').write_text(json.dumps(meta,indent=1));print(json.dumps(meta));assert code==0
