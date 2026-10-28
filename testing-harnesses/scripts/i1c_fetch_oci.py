import hashlib, io, json, gzip, re, sys
from pathlib import Path
from urllib.request import Request, build_opener, HTTPRedirectHandler
from urllib.parse import urlsplit

if len(sys.argv)!=2:raise SystemExit('usage: i1c_fetch_oci.py cache-directory')
root=Path(sys.argv[1]);root.mkdir(parents=True,exist_ok=True)
class Redirect(HTTPRedirectHandler):
 def redirect_request(self,req,fp,code,msg,headers,newurl):
  out=super().redirect_request(req,fp,code,msg,headers,newurl)
  if out is not None and urlsplit(newurl).netloc!=urlsplit(req.full_url).netloc:out.remove_header('Authorization')
  return out
opener=build_opener(Redirect())
def request(url,headers=None,cap=8<<20):
 with opener.open(Request(url,headers=headers or {}),timeout=30) as response:
  data=response.read(cap+1)
  if len(data)>cap:raise RuntimeError('download size limit')
  return data
manifest=root/'REAL_REGISTRY.json'
if manifest.exists():
 rows=json.loads(manifest.read_text())
 for row in rows:
  index=(root/(row['tag']+'.index.json')).read_bytes()
  if 'sha256:'+hashlib.sha256(index).hexdigest()!=row['index_digest']:raise RuntimeError('cached index digest changed')
  for d in [row['manifest'],row['config'],*row['layers']]:
   if re.fullmatch('sha256:[0-9a-f]{64}',d['digest']) is None:raise RuntimeError('invalid cached digest')
   p=root/'blobs'/d['digest'].split(':')[1]
   raw=p.read_bytes()
   if len(raw)!=d['size'] or 'sha256:'+hashlib.sha256(raw).hexdigest()!=d['digest']:raise RuntimeError('cached blob identity changed')
   if 'diff_id' in d:
    h=hashlib.sha256();n=0
    with gzip.GzipFile(fileobj=io.BytesIO(raw)) as stream:
     while data:=stream.read(64<<10):
      n+=len(data)
      if n>32<<20:raise RuntimeError('cached DiffID inflation budget')
      h.update(data)
    if n!=d['plain_bytes'] or 'sha256:'+h.hexdigest()!=d['diff_id']:raise RuntimeError('cached DiffID changed')
 print('reuse hash/DiffID-verified OCI cache')
 raise SystemExit(0)
token=json.loads(request('https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/busybox:pull'))['token']
headers={'Authorization':'Bearer '+token,'Accept':','.join(['application/vnd.oci.image.index.v1+json','application/vnd.docker.distribution.manifest.list.v2+json','application/vnd.oci.image.manifest.v1+json','application/vnd.docker.distribution.manifest.v2+json'])}
base='https://registry-1.docker.io/v2/library/busybox/'
def digest(data):return 'sha256:'+hashlib.sha256(data).hexdigest()
def blob(d,kind='blobs'):
 name=d['digest']
 if re.fullmatch('sha256:[0-9a-f]{64}',name) is None:raise RuntimeError('unsupported digest')
 path=root/'blobs'/name.split(':')[1];path.parent.mkdir(exist_ok=True)
 data=path.read_bytes() if path.exists() else request(base+kind+'/'+name,headers)
 if len(data)!=d['size'] or digest(data)!=name:raise RuntimeError('descriptor verification failure')
 if not path.exists():path.write_bytes(data)
 return data
rows=[];total=0
for tag in ['1.36.1','1.37.0']:
 indexRaw=request(base+'manifests/'+tag,headers,4<<20);index=json.loads(indexRaw)
 indexDigest=digest(indexRaw);(root/(tag+'.index.json')).write_bytes(indexRaw)
 chosen=next(d for d in index['manifests'] if d.get('platform',{}).get('architecture')=='amd64' and d.get('platform',{}).get('os')=='linux')
 doc=json.loads(blob(chosen,'manifests'));cfg=json.loads(blob(doc['config']))
 diffs=cfg['rootfs']['diff_ids'];layers=doc['layers']
 if len(diffs)!=len(layers):raise RuntimeError('DiffID count mismatch')
 row={'tag':tag,'class':'REAL','source':'docker.io/library/busybox','index_digest':indexDigest,'manifest':chosen,'config':doc['config'],'layers':[]}
 for expected,layer in zip(diffs,layers):
  raw=blob(layer);total+=len(raw)
  if total>20<<20:raise RuntimeError('corpus size budget exceeded')
  if not layer['mediaType'].endswith('gzip'):raise RuntimeError('producer outside requested gzip evidence')
  h=hashlib.sha256();n=0
  with gzip.GzipFile(fileobj=io.BytesIO(raw)) as stream:
   while data:=stream.read(64<<10):
    n+=len(data)
    if n>32<<20:raise RuntimeError('DiffID inflation budget exceeded')
    h.update(data)
  if expected!='sha256:'+h.hexdigest():raise RuntimeError('DiffID mismatch')
  row['layers'].append({**layer,'plain_bytes':n,'diff_id':expected,'verified':True})
 rows.append(row)
manifest.write_text(json.dumps(rows,indent=2)+'\n')
print('verified real registry histories:',len(rows),'compressed layer bytes:',total)
for row in rows:print(row['tag'],row['manifest']['digest'],[(x['size'],x['plain_bytes']) for x in row['layers']])
