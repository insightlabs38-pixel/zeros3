"""Bounded native broad-tail producers/oracles; never used by recipe decoders.
Usage: CACHE; PYTHONPATH points at the hash-pinned Q16 producer packages.
"""
import base64,hashlib,io,json,os,pathlib,random,struct,subprocess,sys
import brotli,fastavro,numpy as np,onnx
from fontTools.ttLib import TTFont,woff2
cache=pathlib.Path(sys.argv[1]);out=cache/'q16-corpus';out.mkdir(exist_ok=True);inputs=[]
def sha(b):return hashlib.sha256(b).hexdigest()
def add(family,stage,p,cls,spans=(),logical=(),**extra):
 b=p.read_bytes();assert 0<len(b)<=128<<20;oracles=[]
 for n,value in enumerate(logical):
  q=p.with_name(p.name+f'.logical{n}');q.write_bytes(value);oracles.append(dict(path=str(q),bytes=len(value),sha256=sha(value)))
 inputs.append(dict(family=family,stage=stage,path=str(p),file=p.name,sha256=sha(b),bytes=len(b),class_=cls,resource_spans=spans,chunk_spans=spans,oracles=oracles,**extra))
# The native reader supplies the compressed span and transformed table bytes.
class Trace(io.BytesIO):
 def __init__(self,b):super().__init__(b);self.reads=[]
 def read(self,n=-1):off=self.tell();b=super().read(n);self.reads.append((off,n,len(b)));return b
fonts=out/'woff2';fonts.mkdir(exist_ok=True)
for ver in ['5.15.3','5.15.4','6.0.0']:
 p=cache/'q16-download'/f'fontawesome-{ver}.woff2';b=p.read_bytes();stream=Trace(b);reader=woff2.WOFF2Reader(stream);start,n,_=next(x for x in stream.reads if x[1]==reader.totalCompressedSize);plain=reader.transformBuffer.getvalue();tables=[plain[t.offset:t.offset+t.length] for t in reader.tables.values()]
 # Exercise the complete native inverse font transforms, not only Brotli.
 font=TTFont(io.BytesIO(b),recalcBBoxes=False,recalcTimestamp=False);font.flavor=None;sfnt=io.BytesIO();font.save(sfnt);assert TTFont(io.BytesIO(sfnt.getvalue())).getGlyphOrder()
 add('woff2-releases',ver,p,'REAL_RELEASE_FONT_HISTORY',[[start,n]],tables,native_sfnt_sha256=sha(sfnt.getvalue()))
 if ver=='5.15.4':
  assert reader.metaOffset==reader.privOffset==0
  for quality in [5,11]:
   packed=brotli.compress(plain,quality=quality);new=bytearray(b[:start]+packed);struct.pack_into('>I',new,8,len(new));struct.pack_into('>I',new,20,len(packed));q=fonts/f'reexport-q{quality}.woff2';q.write_bytes(new);again=woff2.WOFF2Reader(io.BytesIO(new));assert again.transformBuffer.getvalue()==plain
   add('woff2-native-reexports',f'q{quality}',q,'SAME_INPUT_REEXPORT_NOT_NATURAL_HISTORY',[[start,len(packed)]],tables)
# Avro native block offsets and decoded record-buffer oracles, with full row proof.
avro=out/'avro';avro.mkdir(exist_ok=True);schema={'type':'record','name':'Row','fields':[{'name':'id','type':'long'},{'name':'label','type':'string'},{'name':'payload','type':'bytes'},{'name':'value','type':'double'}]};rng=random.Random(1044);base=[dict(id=n,label=f'row-{n%257}',payload=rng.randbytes(32),value=n/32) for n in range(16384)]
for codec in ['null','deflate']:
 for stage in ['base','cell','append','metadata','sync-repack','field-order']:
  rows=[dict(r) for r in base];sch=schema;metadata={};interval=32768
  if stage=='cell':rows[8192]['value']+=1
  if stage=='append':rows+=base[:256]
  if stage=='metadata':metadata={'owner':'changed metadata'}
  if stage=='sync-repack':interval=131072
  if stage=='field-order':sch=dict(schema,fields=list(reversed(schema['fields'])))
  p=avro/f'{codec}-{stage}.avro'
  with p.open('wb') as f:fastavro.writer(f,sch,rows,codec=codec,sync_interval=interval,sync_marker=bytes(range(16)))
  with p.open('rb') as f:assert list(fastavro.reader(f))==rows
  spans=[];logical=[]
  with p.open('rb') as f:
   for block in fastavro.block_reader(f):spans.append([block.offset,block.size]);logical.append(block.bytes_.getvalue())
  add('avro-'+codec,stage,p,'NATIVE_PRODUCER_WORKLOAD',spans,logical,rows=len(rows))
# Real model seed plus native graph/tensor order and small edit workloads.
models=out/'onnx';models.mkdir(exist_ok=True);seed=cache/'q16-download/squeezenet1.0-12.onnx';original=onnx.load(seed);onnx.checker.check_model(original)
for stage in ['base','metadata','tensor-cell','initializer-order','node-names','restore']:
 model=onnx.ModelProto();model.CopyFrom(original)
 if stage=='metadata':model.doc_string='ZeroS3 producer metadata-only edit'
 if stage=='initializer-order':values=list(model.graph.initializer);del model.graph.initializer[:];model.graph.initializer.extend(reversed(values))
 if stage=='node-names':
  for i,node in enumerate(model.graph.node):node.name=f'node-{i}'
 if stage=='tensor-cell':
  tensor=next(t for t in model.graph.initializer if t.data_type==onnx.TensorProto.FLOAT and len(t.raw_data)>128);b=bytearray(tensor.raw_data);value=struct.unpack_from('<f',b,0)[0];struct.pack_into('<f',b,0,value+0.01);tensor.raw_data=bytes(b)
 onnx.checker.check_model(model);p=models/(stage+'.onnx');p.write_bytes(model.SerializeToString());b=p.read_bytes();spans=[];logical=[]
 for tensor in model.graph.initializer:
  raw=tensor.raw_data
  if not raw:continue
  logical.append(raw)
  if len(raw)>=128:
   off=b.find(raw)
   if off>=0 and b.find(raw,off+1)<0:spans.append([off,len(raw)])
 spans.sort();assert all(a[0]+a[1]<=z[0] for a,z in zip(spans,spans[1:]));add('onnx-squeezenet',stage,p,'REAL_MODEL_NATIVE_EDIT_WORKLOAD_NOT_NATURAL_HISTORY',spans,logical,initializers=len(model.graph.initializer),native_checker=True)
# Native archives of pinned real release sources; compression is an oracle gap.
archives=out/'7z';archives.mkdir(exist_ok=True);tool=cache/'q16-tools/usr/bin/7zz'
for profile in ['copy','lzma2-solid','lzma2-nonsolid']:
 for rev in ['v1.2.0','v1.3.0','v1.4.0']:
  source=cache/'q15-corpus/hiredis'/rev;files=sorted(p.name for p in source.iterdir() if p.suffix in ['.c','.h']);p=archives/f'{profile}-{rev}.7z'
  if p.exists():p.unlink()
  opts=['-m0=Copy','-ms=off'] if profile=='copy' else ['-m0=LZMA2','-mx=5','-ms='+('on' if profile.endswith('-solid') else 'off')]
  subprocess.run([str(tool),'a','-t7z','-mmt=1','-mhc=off','-mtc=off','-mta=off',*opts,str(p),*files],cwd=source,check=True,stdout=subprocess.DEVNULL,timeout=60)
  subprocess.run([str(tool),'t',str(p)],check=True,stdout=subprocess.DEVNULL,timeout=60)
  logical=[]
  for name in files:
   data=subprocess.check_output([str(tool),'x','-so',str(p),name],timeout=30);assert data==(source/name).read_bytes();logical.append(data)
  b=p.read_bytes();next_off,next_len=struct.unpack_from('<QQ',b,12);assert 32+next_off+next_len==len(b);add('7z-'+profile,rev,p,'REAL_RELEASE_SOURCE_NATIVE_ARCHIVE_NOT_PUBLISHED_ARCHIVE_HISTORY',[[32,next_off]],logical,native_test=True)
(out/'native-inputs.json').write_text(json.dumps(inputs,indent=1));(out/'versions.json').write_text(json.dumps(dict(brotli=brotli.__version__,fastavro=fastavro.__version__,onnx=onnx.__version__,numpy=np.__version__,seven_zip=subprocess.check_output([str(tool),'i'],text=True).splitlines()[1]),indent=1));print(json.dumps(dict(inputs=len(inputs),families=sorted({r['family'] for r in inputs}))))
