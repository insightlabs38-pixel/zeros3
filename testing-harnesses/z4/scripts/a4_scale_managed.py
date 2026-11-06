from pathlib import Path
import subprocess,json,time,hashlib
exe='/workspace/work/z4-a4/managed';root=Path('/workspace/work/z4-a4/scale-at-final');root.mkdir(exist_ok=True)
inputs=[('newtonsoft',Path('/workspace/work/q6-public/clr/13.0.4.dll')),('fsharp',Path('/workspace/work/q9-fsharp/canonical-build/out/FSharp.Core.dll')),('corelib',Path('/workspace/work/q4/dotnet/shared/Microsoft.NETCore.App/8.0.33/System.Private.CoreLib.dll')),('roslyn',Path('/workspace/work/q4/dotnet/sdk/8.0.427/Roslyn/bincore/Microsoft.CodeAnalysis.CSharp.dll'))]
rows=[]
for name,p in inputs:
 print(name,p.stat().st_size,flush=True)
 result={'name':name,'input':str(p),'bytes':p.stat().st_size,'sha256':hashlib.sha256(p.read_bytes()).hexdigest(),'samples':[]}
 for mode in ['stream-at','proof','legacy']:
  out=root/(name+('-legacy' if mode=='legacy' else '-stream'))
  cp=subprocess.run([exe,mode,str(p),str(out)],text=True,capture_output=True,timeout=180)
  (root/(name+'-'+mode+'.stdout')).write_text(cp.stdout);(root/(name+'-'+mode+'.stderr')).write_text(cp.stderr)
  record={'mode':mode,'returncode':cp.returncode}
  if cp.returncode==0:record.update(json.loads(cp.stdout))
  result['samples'].append(record)
  print(name,mode,record,flush=True)
 if all(x['returncode']==0 for x in result['samples']):
  a,b=result['samples'][0],result['samples'][2]
  assert all(a[k]==b[k] for k in ['components','meta_sha256','stream_sha256'])
  result['legacy_exact_components']=True
 rows.append(result);(root/'rows.json').write_text(json.dumps(rows,indent=2))
