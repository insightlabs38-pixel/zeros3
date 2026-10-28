"""Independent numeric LLVM operand verification; blobs are counted separately.
Usage: CACHE FRAME_CLI. Each ordinary decoded record hash must match exactly.
Blob bytes remain covered by full serialized proofs; this checks numeric operands.
"""
import hashlib,json,os,pathlib,re,struct,subprocess,sys
cache=pathlib.Path(sys.argv[1]);frame=sys.argv[2];env=dict(os.environ,LD_LIBRARY_PATH=str(cache/'q4/llvm/usr/lib/x86_64-linux-gnu'));rows=json.loads((cache/'q15-corpus/inputs.json').read_text());results=[]
for r in rows:
 p=pathlib.Path(r['path']);dump=p.with_suffix('.numeric.dump')
 with dump.open('w') as f:subprocess.run([str(cache/'q4/llvm/usr/bin/llvm-bcanalyzer-19'),'--dump','--dump-blockinfo','--non-symbolic',str(p)],env=env,stdout=f,check=True,timeout=60)
 process=subprocess.Popen([frame,str(p),'--records'],stdout=subprocess.PIPE,text=True);stack=[];checked=blobs=0
 with dump.open() as f:
  for line in f:
   line=line.strip()
   if line.startswith('</'):stack.pop();continue
   block=re.match(r'<[^ ]+ BlockID=(\d+)',line) or re.match(r'<UnknownBlock(\d+)',line)
   if block:
    if not line.split('>')[0].endswith('/'):stack.append(int(block[1]))
    continue
   code=re.search(r'codeid=(\d+)',line) or re.match(r'<UnknownCode(\d+)',line)
   if code is None:continue
   actual=json.loads(process.stdout.readline());assert actual['Block']==stack[-1] and actual['Code']==int(code[1]),(p,line[:200],actual,stack)
   if 'blob data =' in line or 'num-strings =' in line:blobs+=1;continue
   pairs=re.findall(r'\bop(\d+)=(-?\d+)',line);assert [int(i) for i,_ in pairs]==list(range(len(pairs)))
   h=hashlib.sha256();h.update(struct.pack('<Q',int(code[1])))
   for _,v in pairs:h.update(struct.pack('<Q',int(v)&((1<<64)-1)))
   assert actual['Values']==len(pairs) and actual['SHA']==h.hexdigest(),(p,line[:200],actual)
   checked+=1
 assert not process.stdout.readline();assert process.wait(timeout=30)==0 and not stack
 results.append(dict(family=r['family'],stage=r['stage'],numeric_records=checked,blob_records_byteproof_only=blobs,native_dump_sha256=hashlib.sha256(dump.read_bytes()).hexdigest()))
(cache/'q15-corpus/operand-comparison.json').write_text(json.dumps(results,indent=1));print(json.dumps(dict(inputs=len(results),numeric_records=sum(r['numeric_records'] for r in results),blob_records=sum(r['blob_records_byteproof_only'] for r in results),status='PASS')))
