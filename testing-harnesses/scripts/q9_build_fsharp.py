"""Rebuild a controlled F# history in a fixed output path. ROOT DOTNET_ROOT CACHE."""
import hashlib, json, pathlib, shutil, subprocess, sys
root, dotnet, cache = map(pathlib.Path, sys.argv[1:]); build=cache/'canonical-build'; build.mkdir(parents=True,exist_ok=True)
src=root/'testing-harnesses/testdata/i2-draft/fsharp'; core=dotnet/'sdk/8.0.427/FSharp/FSharp.Core.dll'
project=(src/'Sample.fsproj.template').read_text().replace('__FSHARP_CORE__',str(core)).replace('<Optimize>true</Optimize>',f'<Optimize>true</Optimize><PathMap>{cache}=/_/q9-fsharp</PathMap>')
(build/'Sample.fsproj').write_text(project)
rows=[]
for revision in range(2):
    source=(src/'Sample.fs').read_text().replace('n + 3',f'n + {3+revision}')
    (build/'Sample.fs').write_text(source)
    cmd=[str(dotnet/'dotnet'),'build',str(build/'Sample.fsproj'),'-c','Release','-o',str(build/'out'),'--nologo','--ignore-failed-sources']
    subprocess.run(cmd,check=True)
    target=cache/f'canonical-r{revision}'/'Sample.dll'; target.parent.mkdir(exist_ok=True); shutil.copyfile(build/'out/Sample.dll',target)
    rows.append(dict(revision=revision,file=str(target),sha256=hashlib.sha256(target.read_bytes()).hexdigest(),bytes=target.stat().st_size,source_sha256=hashlib.sha256(source.encode()).hexdigest(),evidence='PRODUCER_WORKLOAD',mutation=f'Busy n -> n + {3+revision}'))
json.dump(dict(sdk='8.0.427',core_sha256=hashlib.sha256(core.read_bytes()).hexdigest(),compiler_sha256=hashlib.sha256((core.parent/'fsc.dll').read_bytes()).hexdigest(),fixed_output=True,pathmap='/_/q9-fsharp',revisions=rows),open(cache/'canonical-provenance.json','w'),indent=1)
