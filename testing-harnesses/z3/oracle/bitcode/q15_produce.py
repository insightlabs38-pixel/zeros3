"""Compile pinned real C project releases and controlled C/C++ edits.
Usage: CACHE; LLVM 19 is the cached Q4 producer/oracle, never a decoder.
"""
import concurrent.futures,hashlib,json,os,pathlib,subprocess,sys
cache=pathlib.Path(sys.argv[1]);root=cache/'q15-corpus';tools=cache/'q4/llvm/usr/bin';env=dict(os.environ,LD_LIBRARY_PATH=str(cache/'q4/llvm/usr/lib/x86_64-linux-gnu'));jobs=[]
flags=['-emit-llvm','-c','-g0','-fno-ident','-fno-discard-value-names',f'-ffile-prefix-map={root}=/src','-target','x86_64-pc-linux-gnu']
for rev in ['v1.0.0','v1.1.0','v1.2.0','v1.3.0','v1.4.0']:
 for file in ['hiredis.c','read.c','sds.c']:
  for opt in ['-O0','-O2']:jobs.append(dict(project='hiredis',family=f'hiredis-{file}-{opt[1:]}',stage=rev,source=str(root/'hiredis'/rev/file),flags=[opt],class_='REAL_RELEASE_SOURCE_COMPILED_HISTORY'))
for rev in ['3450300','3460100','3490100']:
 jobs.append(dict(project='sqlite',family='sqlite-O2',stage=rev,source=str(root/'sqlite'/rev/'sqlite3.c'),flags=['-O2','-DSQLITE_THREADSAFE=0','-DSQLITE_OMIT_LOAD_EXTENSION','-DSQLITE_DEFAULT_MEMSTATUS=0'],class_='REAL_RELEASE_SOURCE_COMPILED_HISTORY'))
controls=root/'controls';controls.mkdir(exist_ok=True)
base='struct Pair{int x; long y;};\nint square(int x){return x*x;}\nint sum(struct Pair p){return p.x+(int)p.y;}\n'
cases={'base':base,'constant':base.replace('x*x','x*x+1'),'early-function':'int early(int x){return x+11;}\n'+base,'late-function':base+'int late(int x){return x+11;}\n','reorder':'struct Pair{int x; long y;};\nint sum(struct Pair p){return p.x+(int)p.y;}\nint square(int x){return x*x;}\n','type-churn':'struct Other{double z[4];};\nstruct Other other;\n'+base,'rename':base.replace('square','squared'),'restore':base}
for language in ['c','cc']:
 for stage,text in cases.items():
  source=controls/f'{stage}.{language}';source.write_text(text)
  for opt in ['-O0','-O2']:jobs.append(dict(project='controlled',family=f'controlled-{language}-{opt[1:]}',stage=stage,source=str(source),flags=[opt],class_='CONTROLLED_IR_EDIT'))
def run(j):
 out=root/'bitcode'/j['family']/j['stage'];out.parent.mkdir(parents=True,exist_ok=True);bc=pathlib.Path(str(out)+'.bc');source=pathlib.Path(j['source']);compile_source=source
 if j['project']=='controlled':
  context=out.parent/(j['stage']+'-context');context.mkdir(exist_ok=True);compile_source=context/('input'+source.suffix);subprocess.run(['cp',str(source),str(compile_source)],check=True)
 cmd=[str(tools/('clang++-19' if source.suffix=='.cc' else 'clang-19')),*flags,*j['flags'],compile_source.name,'-o',str(bc)]
 subprocess.run(cmd,cwd=compile_source.parent,env=env,check=True,capture_output=True,timeout=180)
 dump=subprocess.run([str(tools/'llvm-bcanalyzer-19'),'-dump',str(bc)],env=env,check=True,capture_output=True,timeout=60);bc.with_suffix('.dump').write_bytes(dump.stdout)
 ir=subprocess.run([str(tools/'llvm-dis-19'),str(bc),'-o','-'],env=env,check=True,capture_output=True,timeout=60);bc.with_suffix('.ll').write_bytes(ir.stdout)
 b=bc.read_bytes();assert len(b)<=128<<20
 return dict(j,path=str(bc),file=bc.name,sha256=hashlib.sha256(b).hexdigest(),bytes=len(b),source_sha256=hashlib.sha256(source.read_bytes()).hexdigest(),command=cmd,compile_cwd=str(compile_source.parent),oracle_dump_sha256=hashlib.sha256(dump.stdout).hexdigest(),ir_sha256=hashlib.sha256(ir.stdout).hexdigest())
with concurrent.futures.ThreadPoolExecutor(max_workers=2) as executor:rows=list(executor.map(run,jobs))
(root/'inputs.json').write_text(json.dumps(rows,indent=1));print(json.dumps(dict(inputs=len(rows),bytes=sum(r['bytes'] for r in rows),compiler=subprocess.check_output([str(tools/'clang-19'),'--version'],env=env,text=True))))
