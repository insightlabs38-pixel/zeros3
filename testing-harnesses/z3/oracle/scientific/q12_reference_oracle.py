"""Read each pinned reference with its native library; retain all reader outcomes."""
import hashlib,json,pathlib,sys
import h5py,pyarrow as pa,pyarrow.ipc as ipc,pyorc
cache,manifest=map(pathlib.Path,sys.argv[1:3]);rows=[]
for x in json.loads(manifest.read_text()):
 p=cache/x['file'];b=p.read_bytes();assert hashlib.sha256(b).hexdigest()==x['sha256'];r=dict(file=p.name,sha256=x['sha256'])
 try:
  if x['family']=='arrow-reference':
   reader=ipc.open_file(pa.BufferReader(b)) if b[:6]==b'ARROW1' else ipc.open_stream(pa.BufferReader(b));t=reader.read_all();r.update(rows=t.num_rows,columns=t.num_columns,schema_sha256=hashlib.sha256(t.schema.serialize()).hexdigest())
  elif x['family']=='hdf5-reference':
   with h5py.File(p,'r') as f:
    objects=[]
    def visit(name,d):
     if isinstance(d,h5py.Dataset):
      a=d[()];objects.append(dict(name=name,shape=d.shape,dtype=str(d.dtype),value_read=True))
    f.visititems(visit);r['datasets']=objects
  else:
   with open(p,'rb') as f:
    reader=pyorc.Reader(f);count=0
    for _ in reader:
     count+=1
     if count>10_000_000:raise ValueError('reference row oracle bound')
    r.update(rows=count,schema=str(reader.schema))
  r['read']=True
 except Exception as e:r.update(read=False,error=type(e).__name__+': '+str(e))
 rows.append(r)
print(json.dumps(dict(versions=dict(h5py=h5py.__version__,hdf5=h5py.version.hdf5_version,pyarrow=pa.__version__,pyorc=pyorc.__version__),references=rows),indent=1))
