"""Controlled C++ native histories: script CACHE GXX CLANGXX CLANG LLD_LINK."""
from pathlib import Path
import os,sys,subprocess,json,hashlib,shutil
if len(sys.argv)!=6:raise SystemExit(__doc__)
root=Path(sys.argv[1]).resolve();root.mkdir(parents=True,exist_ok=True);build=root/'build';build.mkdir(exist_ok=True)
gxx,cxx,clang,link=sys.argv[2:];commands=[];rows=[]
def run(args):
 commands.append(args);r=subprocess.run(args,cwd=build,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
 if r.returncode:raise RuntimeError(r.stdout)
for rev in range(3):
 out=root/f'rev{rev}';out.mkdir(exist_ok=True)
 src='struct Base { virtual int eval(int)=0; };\n'
 src+='template<int N> struct Node: Base { __attribute__((noinline)) int eval(int x) override { return x*3+N+((N==50)?'+str(rev if rev<2 else 0)+':0); } };\n'
 if rev==2:src+='__attribute__((noinline)) int early(int x){return x+99;}\n'
 src+='int main(){\n'+'\n'.join(f'Node<{i}> n{i};' for i in range(64))+'\nBase* nodes[]={'+','.join(f'&n{i}' for i in range(64))+'};\nint sum=0;for(int i=0;i<64;i++)sum+=nodes[i]->eval(i);'
 if rev==2:src+='sum+=early(sum);'
 src+='return sum;}\n';(build/'sample.cpp').write_text(src)
 for name,tool in [('gcc',gxx),('clang',cxx)]:
  run([tool,'-g','-O1','-fno-pie','-no-pie','-fno-rtti','-fno-exceptions',f'-ffile-prefix-map={build}=/src','sample.cpp','-o',name+'-cpp'])
  shutil.copyfile(build/(name+'-cpp'),out/(name+'-cpp'))
  run(['objcopy','--strip-all',name+'-cpp',name+'-cpp-strip']);shutil.copyfile(build/(name+'-cpp-strip'),out/(name+'-cpp-strip'))
 run([clang,'--target=x86_64-pc-windows-msvc','-g','-gcodeview','-O1','-fno-rtti','-fno-exceptions',f'-fdebug-prefix-map={build}=/src','-c','sample.cpp','-o','sample.obj'])
 run([link,'/entry:main','/subsystem:console','/nodefaultlib','/debug','/Brepro','/pdbsourcepath:/src','/pdb:sample.pdb','/out:sample.exe','sample.obj']);shutil.copyfile(build/'sample.exe',out/'clang-cpp.exe');shutil.copyfile(build/'sample.cpp',out/'sample.cpp')
 for f in out.iterdir():
  b=f.read_bytes();rows.append({'class':'PRODUCER_WORKLOAD','file':str(f.relative_to(root)),'sha256':hashlib.sha256(b).hexdigest(),'bytes':len(b)})
(root/'provenance.json').write_text(json.dumps({'commands':commands,'files':rows},indent=2)+'\n')
