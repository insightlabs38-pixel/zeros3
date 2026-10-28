"""Controlled producer workloads; explicit tool paths, fixed build directory.
Usage: q4_debug_builds.py CACHE GCC CLANG LLD_LINK DOTNET
"""
import hashlib,json,os,shutil,subprocess,sys
from pathlib import Path
if len(sys.argv)!=6:raise SystemExit(__doc__)
root=Path(sys.argv[1]).resolve();root.mkdir(parents=True,exist_ok=True)
gcc,clang,link,dotnet=sys.argv[2:];build=root/'build';build.mkdir(exist_ok=True)
env={**os.environ,'DOTNET_CLI_HOME':str(root/'dotnet-cli'),'DOTNET_SKIP_FIRST_TIME_EXPERIENCE':'1','DOTNET_CLI_TELEMETRY_OPTOUT':'1'}
commands=[];rows=[]
def run(args):
 commands.append(args)
 r=subprocess.run(args,cwd=build,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
 if r.returncode:raise RuntimeError(r.stdout)
 return r.stdout
for rev in range(5):
 out=root/f'rev{rev}';out.mkdir(exist_ok=True)
 body='\n'.join(f'int func_{i}(int x) {{ int local=x+{i+(rev if i==50 and rev<3 else 0)}; return local * 3; }}' for i in range(200))
 if rev==3:body='int early_insert(int x) { return x+1; }\n'+body
 if rev==4:body=body.replace('int func_0','int early_insert(int x) { return x+1; }\nint func_0')
 c=body+'\nint main(void){return func_50(1);}\n';(build/'sample.c').write_text(c)
 methods='\n'.join(f'public static int Method{i}(int x) {{ int local=x+{i+(rev if i==50 and rev<3 else 0)}; return local*3; }}' for i in range(200))
 if rev==3:methods='public static int EarlyMethod(int x) => x+1;\n'+methods
 prefix='public class AFirst { public static int Added(int x)=>x; }\n' if rev==4 else ''
 cs='namespace DebugHistory;\n'+prefix+'public static class Sample {\n'+methods+'\n}'
 (build/'Sample.cs').write_text(cs)
 (build/'Sample.csproj').write_text('<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework><OutputType>Library</OutputType><DebugType>portable</DebugType><Deterministic>true</Deterministic><PathMap>'+str(build)+'=/src</PathMap></PropertyGroup></Project>')
 for version in [4,5]:run([gcc,'-g',f'-gdwarf-{version}','-O1','-fno-pie','-no-pie',f'-ffile-prefix-map={build}=/src','sample.c','-o',f'gcc-dwarf{version}'])
 run([clang,'-g','-gdwarf-5','-O1','-fno-pie','-no-pie',f'-ffile-prefix-map={build}=/src','sample.c','-o','clang-dwarf5'])
 run([clang,'--target=x86_64-pc-windows-msvc','-g','-gcodeview','-O1',f'-fdebug-prefix-map={build}=/src','-c','sample.c','-o','sample.obj'])
 run([link,'/debug','/entry:main','/subsystem:console','/nodefaultlib','/Brepro','/pdbsourcepath:/src','/pdb:sample.pdb','/out:sample.exe','sample.obj'])
 (out/'dotnet-build.log').write_text(run([dotnet,'build','Sample.csproj','--nologo','--no-incremental']))
 files=['gcc-dwarf4','gcc-dwarf5','clang-dwarf5','sample.pdb','sample.exe','sample.c','Sample.cs','Sample.csproj']
 for name in files:shutil.copyfile(build/name,out/name)
 for suffix in ['dll','pdb']:shutil.copyfile(build/f'bin/Debug/net8.0/Sample.{suffix}',out/f'portable.{suffix}')
 for file in sorted(out.iterdir()):
  if file.name.endswith('.log'):continue
  b=file.read_bytes();rows.append({'class':'PRODUCER_WORKLOAD','revision':rev,'file':str(file.relative_to(root)),'bytes':len(b),'sha256':hashlib.sha256(b).hexdigest()})
versions={name:subprocess.run([tool,'--version'],env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True).stdout.splitlines()[0] for name,tool in [('gcc',gcc),('clang',clang),('lld-link',link),('dotnet',dotnet)]}
(root/'provenance.json').write_text(json.dumps({'versions':versions,'commands':commands,'files':rows,'edits':['baseline','one numeric body edit','second body edit','early method/function insertion','early type insertion']},indent=2)+'\n')
