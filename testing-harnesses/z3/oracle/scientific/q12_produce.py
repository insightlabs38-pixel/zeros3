"""Bounded native producer histories and byte-span/logical oracles, never a decoder.
Usage: PYTHONPATH=isolated-deps python q12_produce.py CACHE.
All histories here are PRODUCER_WORKLOAD; reexports are explicitly separate.
"""
import hashlib,json,pathlib,shutil,sys
import numpy as np
import h5py,pyorc,pyarrow as pa,pyarrow.ipc as ipc,pyarrow.orc as orc
assert np.__version__=="2.3.5"
root=pathlib.Path(sys.argv[1]);root.mkdir(parents=True,exist_ok=True)
assert h5py.__version__=='3.15.1' and pyorc.__version__=='0.10.0' and pa.__version__=='18.1.0'
N=262144
rng=np.random.RandomState(1040)
base=np.column_stack([np.arange(N,dtype=np.int64),rng.randint(0,1<<31,N,dtype=np.int64),rng.randint(0,1<<31,N,dtype=np.int64),rng.randint(0,8,N,dtype=np.int64)])
variants=[]
variants.append(('r0',base.copy(),'base'))
a=base.copy();a[123456,1]=31;variants.append(('r1-one-cell',a,'one cell update'))
a=a.copy();a[::997,2]=99;variants.append(('r2-sparse',a,'sparse cells'))
extra=np.column_stack([np.arange(N,N+32768,dtype=np.int64),rng.randint(0,1<<31,32768,dtype=np.int64),rng.randint(0,1<<31,32768,dtype=np.int64),rng.randint(0,8,32768,dtype=np.int64)])
a=np.concatenate([a,extra]);variants.append(('r3-append',a,'append32768 via native rewrite'))
a=np.delete(a,np.s_[130000:134096],axis=0);variants.append(('r4-delete',a,'delete4096 middle via native rewrite'))
variants.append(('r5-metadata',a.copy(),'metadata-only'))
variants.append(('r6-repack',a.copy(),'repack geometry and remove probe metadata'))
variants.append(('r7-rewrite',a.copy(),'rewrite using original geometry after repack'))
rows=[]
def digest(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def record(path,family,stage,mutation,spans,arr,klass='PRODUCER_WORKLOAD'):
 b=path.read_bytes();assert len(b)<=16<<20
 spans=sorted(set((int(o),int(n)) for o,n in spans if n))
 end=0
 for off,n in spans:assert off>=end and n>0 and off+n<=len(b);end=off+n
 logical=[]
 for i in range(4):
  p=root/'logical'/stage/('col'+str(i)+'.bin');p.parent.mkdir(parents=True,exist_ok=True)
  raw=np.ascontiguousarray(arr[:,i],dtype='<i8').tobytes()
  if p.exists():assert p.read_bytes()==raw
  else:p.write_bytes(raw)
  logical.append(dict(path=str(p),bytes=len(raw),sha256=digest(p)))
 rows.append(dict(path=str(path),family=family,stage=stage,mutation=mutation,class_=klass,sha256=hashlib.sha256(b).hexdigest(),bytes=len(b),spans=spans,oracles=logical))
def table(a,meta=False):
 t=pa.table({name:pa.array(a[:,i]) for i,name in enumerate(['id','a','b','flag'])})
 return t.replace_schema_metadata({b'job':b'q12-metadata-change'}) if meta else t
for stage,a,mutation in variants:
 for comp in ['none','gzip']:
  p=root/('hdf5-'+comp)/stage/'table.h5';p.parent.mkdir(parents=True,exist_ok=True)
  with h5py.File(p,'w',libver='latest') as f:
   d=f.create_dataset('table',data=a,chunks=(8192 if stage=='r6-repack' else 4096,4),compression=None if comp=='none' else 'gzip',shuffle=comp!='none')
   if stage=='r5-metadata':f.attrs['job']='q12-metadata-change'
   spans=[(d.id.get_chunk_info(i).byte_offset,d.id.get_chunk_info(i).size) for i in range(d.id.get_num_chunks())]
  record(p,'hdf5-'+comp,stage,mutation,spans,a)
 for producer in ['pyorc','pyarrow']:
  p=root/('orc-'+producer)/stage/'table.orc';p.parent.mkdir(parents=True,exist_ok=True)
  if producer=='pyorc':
   with open(p,'wb') as f:
    w=pyorc.Writer(f,'struct<id:bigint,a:bigint,b:bigint,flag:bigint>',stripe_size=(2<<20 if stage=='r6-repack' else 1<<20),compression=pyorc.CompressionKind.ZLIB)
    if stage=='r5-metadata':w.set_user_metadata(job=b'q12-metadata-change')
    w.writerows(tuple(map(int,r)) for r in a);w.close()
  else:orc.write_table(table(a,stage=='r5-metadata'),p,stripe_size=(2<<20 if stage=='r6-repack' else 1<<20),compression='zlib')
  with open(p,'rb') as f:
   r=pyorc.Reader(f);spans=[(s.bytes_offset,s.bytes_length) for s in r.iter_stripes()];assert sum(1 for _ in r)==len(a)
  record(p,'orc-'+producer,stage,mutation,spans,a)
 for comp in ['none','zstd']:
  p=root/('arrow-'+comp)/stage/'table.arrow';p.parent.mkdir(parents=True,exist_ok=True)
  t=table(a,stage=='r5-metadata')
  with pa.OSFile(str(p),'wb') as f:
   with ipc.new_file(f,t.schema,options=ipc.IpcWriteOptions(compression=None if comp=='none' else comp)) as w:
    for b in t.to_batches(max_chunksize=(65536 if stage=='r6-repack' else 32768)):w.write_batch(b)
  spans=[]
  with pa.memory_map(str(p),'r') as f:
   buf=f.read_buffer();start=buf.address;r=ipc.open_file(buf)
   assert sum(r.get_batch(i).num_rows for i in range(r.num_record_batches))==len(a)
   if comp=='none':
    for i in range(r.num_record_batches):
     for col in r.get_batch(i).columns:
      for b in col.buffers():
       if b is not None and b.size:assert start<=b.address and b.address+b.size<=start+buf.size;spans.append((b.address-start,b.size))
   else:
    # Native message body spans; compressed-buffer boundaries need a strict
    # FlatBuffers parser before becoming a candidate, not an oracle guess.
    f.seek(8)
    while True:
     try:m=ipc.read_message(f)
     except EOFError:break
     if m is None:break
     if m.body is not None and m.body.size:spans.append((m.body.address-start,m.body.size))
  record(p,'arrow-'+comp,stage,mutation,spans,a)
# Actual unlimited-dataset resize is a distinct HDF5 control, not a rewrite.
p=root/'hdf5-unlimited'/'r0'/'table.h5';p.parent.mkdir(parents=True,exist_ok=True)
with h5py.File(p,'w',libver='latest') as f:f.create_dataset('table',data=base,maxshape=(None,4),chunks=(4096,4),compression='gzip')
for stage in ['r0','r1-resize']:
 p=root/'hdf5-unlimited'/stage/'table.h5'
 if stage!='r0':
  p.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(root/'hdf5-unlimited/r0/table.h5',p)
  with h5py.File(p,'r+') as f:d=f['table'];d.resize((N+32768,4));d[N:]=extra
 with h5py.File(p,'r') as f:
  d=f['table'];a=d[:];spans=[(d.id.get_chunk_info(i).byte_offset,d.id.get_chunk_info(i).size) for i in range(d.id.get_num_chunks())]
 record(p,'hdf5-unlimited',stage,'actual native dataset resize' if stage!='r0' else 'unlimited base',spans,a)
(root/'inputs.json').write_text(json.dumps(rows,indent=1))
(root/'producers.json').write_text(json.dumps(dict(numpy=np.__version__,h5py=h5py.__version__,hdf5=h5py.version.hdf5_version,pyorc=pyorc.__version__,pyarrow=pa.__version__,rows=N,class_='PRODUCER_WORKLOAD_NOT_NATURAL_HISTORY'),indent=1))
print('native inputs',len(rows))
