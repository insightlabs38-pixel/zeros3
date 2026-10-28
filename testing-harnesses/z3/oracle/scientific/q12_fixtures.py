"""Freeze independently produced small native fixtures for the bounded parsers."""
import hashlib,json,pathlib,sys
import h5py,numpy as np,pyarrow as pa,pyarrow.ipc as ipc
root=pathlib.Path(sys.argv[1]);root.mkdir(parents=True,exist_ok=True);rows=[]
def record(p,fmt,supported,spans=None):
 b=p.read_bytes();rows.append(dict(file=p.name,format=fmt,supported=supported,sha256=hashlib.sha256(b).hexdigest(),bytes=len(b),spans=spans))
a=np.arange(2048,dtype=np.int64).reshape(-1,4)
for name,comp,libver,unlimited in [('raw',None,'latest',False),('gzip','gzip','latest',False),('unlimited','gzip','latest',True),('legacy',None,'earliest',False)]:
 p=root/('hdf5-'+name+'.h5')
 with h5py.File(p,'w',libver=libver) as f:
  d=f.create_dataset('table',data=a,chunks=(128,4),compression=comp,maxshape=(None,4) if unlimited else a.shape,track_times=False)
  spans=sorted((d.id.get_chunk_info(i).byte_offset,d.id.get_chunk_info(i).size) for i in range(d.id.get_num_chunks()))
 with h5py.File(p,'r') as f:assert np.array_equal(f['table'][:],a)
 record(p,'hdf5',not unlimited and libver=='latest',spans)
t=pa.table({'id':pa.array(np.arange(1024,dtype=np.int64)),'a':pa.array(np.arange(1024,dtype=np.int64)%17)})
d=pa.table({'labels':pa.array(['alpha','beta','gamma','beta']*256).dictionary_encode(),'data':pa.array([b'a',b'bb',b'ccc',None]*256)})
for name,table,stream,codec in [('plain',t,False,None),('zstd',t,False,'zstd'),('dictionary',d,False,'zstd'),('stream',t,True,'zstd'),('lz4',t,False,'lz4')]:
 p=root/('arrow-'+name+('.stream' if stream else '.arrow'))
 with pa.OSFile(str(p),'wb') as f:
  factory=ipc.new_stream if stream else ipc.new_file
  with factory(f,table.schema,options=ipc.IpcWriteOptions(compression=codec)) as w:
   for b in table.to_batches(max_chunksize=256):w.write_batch(b)
 with pa.memory_map(str(p),'r') as f:
  reader=ipc.open_stream(f) if stream else ipc.open_file(f)
  assert reader.read_all().equals(table)
 record(p,'arrow',True)
(root/'fixtures.json').write_text(json.dumps(dict(producer=dict(h5py=h5py.__version__,hdf5=h5py.version.hdf5_version,pyarrow=pa.__version__),fixtures=rows),indent=1))
print('native fixtures',len(rows))
