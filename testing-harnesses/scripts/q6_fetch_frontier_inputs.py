"""Bounded reuse of already-pinned campaign sources; script CACHE Z3_MODULE."""
from pathlib import Path
from urllib.request import urlopen
from concurrent.futures import ThreadPoolExecutor
import hashlib,json,sys,zipfile,io
root=Path(sys.argv[1]).resolve();module=Path(sys.argv[2]).resolve();root.mkdir(parents=True,exist_ok=True)
sources=json.loads((module/'realworld/sources.json').read_text())['sources']
rows=[x for x in sources if x['group']=='pythia-14m'][:3]+[x for x in sources if x['group']=='fdroid-apk'][-3:]
rows += [{**x,'group':'car','name':x['name'],'format':'car'} for x in json.loads((module/'realworld/car.json').read_text())['sources'][:3]]
rows += [{**x,'group':'clr','name':x['version'],'sha256':x['package_sha256'],'size':x['package_size'],'format':'nuget'} for x in json.loads((module/'realworld/clrwasm.json').read_text())['sources'] if x.get('family')=='newtonsoft-netstandard2.0']
def fetch(row):
 directory=root/row['group'];directory.mkdir(exist_ok=True);archive=directory/(row['name']+'.archive');expected=row['sha256']
 try:
  if not archive.exists() or hashlib.sha256(archive.read_bytes()).hexdigest()!=expected:
   with urlopen(row['url'],timeout=40) as r,archive.open('wb') as f:
    n=0;h=hashlib.sha256()
    while b:=r.read(1<<20):
     n+=len(b)
     if n>row['size']:raise RuntimeError('download size budget')
     f.write(b);h.update(b)
   if n!=row['size'] or h.hexdigest()!=expected:raise RuntimeError('native hash mismatch')
  b=archive.read_bytes();name=row['name'];ext='data'
  if row['group'] in ['fdroid-apk','clr']:
   with zipfile.ZipFile(io.BytesIO(b)) as z:
    member='classes.dex' if row['group']=='fdroid-apk' else row['member'];info=z.getinfo(member)
    if info.file_size>32<<20:raise RuntimeError('extracted member budget')
    b=z.read(info)
   ext='dex' if row['group']=='fdroid-apk' else 'dll'
   if row['group']=='clr' and hashlib.sha256(b).hexdigest()!=row['artifact_sha256']:raise RuntimeError('native DLL hash mismatch')
  elif row['group']=='pythia-14m':ext='safetensors'
  elif row['group']=='car':ext='car'
  out=directory/(name+'.'+ext);out.write_bytes(b)
  return {'class':'REAL','source':row,'file':str(out),'bytes':len(b),'sha256':hashlib.sha256(b).hexdigest()}
 except Exception as e:return {'source':row,'blocked':type(e).__name__,'reason':str(e).split('?')[0][:180]}
results=list(ThreadPoolExecutor(3).map(fetch,rows));(root/'provenance.json').write_text(json.dumps(results,indent=2)+'\n')
for row in results:print(row.get('file',row.get('blocked')),row.get('bytes',row.get('reason')))
