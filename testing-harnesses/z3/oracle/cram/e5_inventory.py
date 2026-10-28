"""Native HTSlib boundary oracle; no candidate parser or codec is implemented.
Usage: CACHE SCAN_SO. Source/payload/CRC equality independently checks native spans.
"""
import ctypes,hashlib,json,pathlib,sys,zlib,pysam
cache=pathlib.Path(sys.argv[1]);out=cache/'e5-corpus';public=out/'public';inputs=json.loads((out/'native/inputs.json').read_text())
def signature(r):
 # MD/NM are reference-derived, not a promise of original textual SAM encoding.
 return (r.query_name,r.flag,r.reference_id,r.reference_start,r.mapping_quality,r.cigarstring,r.next_reference_id,r.next_reference_start,r.template_length,r.query_sequence,list(r.query_qualities or []),sorted((k,str(v)) for k,v in r.get_tags() if k not in ['MD','NM']))

for name,sam,ref in [('ce#5b_java.cram','ce#5b.sam','ce.fa'),('auxf#values_java.cram','auxf#values.sam','auxf.fa'),('xx#large_aux_java.cram','xx#large_aux.sam','xx.fa'),('range.cram',None,'ce.fa')]:
 p=public/name
 with pysam.AlignmentFile(str(p),'rc',reference_filename=str(public/ref)) as f:records=list(f)
 mismatch=[]
 if sam:
  with pysam.AlignmentFile(str(public/sam),'r') as f:source=list(f)
  assert len(source)==len(records)
  for a,b in zip(source,records):
   if signature(a)==signature(b):continue
   # Preserve the observed negative: this Java fixture changes H tag types to
   # signed arrays. Bit patterns agree; original SAM tag semantics do not round-trip.
   assert name=='auxf#values_java.cram' and signature(a)[:-1]==signature(b)[:-1],(name,a.query_name)
   old={k:(v,t) for k,v,t in a.get_tags(with_value_type=True) if k not in ['MD','NM']};new={k:(v,t) for k,v,t in b.get_tags(with_value_type=True) if k not in ['MD','NM']};assert set(old)==set(new)
   for tag,(value,typ) in old.items():
    other,other_type=new[tag]
    if str(value)==str(other) and typ==other_type:continue
    # Integer storage widths are not semantic tag-type changes; signature uses
    # their values. H and unsigned-array conversions caused the failed comparison.
    if str(value)==str(other) and typ in 'cCsSiI' and other_type in 'cCsSiI':continue
    if typ=='H':
     assert other_type=='B' and other.typecode=='b' and bytes.fromhex(value)==other.tobytes()
     old_type,new_type='H','B:b'
    else:
     assert typ==other_type=='B' and value.typecode in 'BHI' and other.typecode=={'B':'b','H':'h','I':'i'}[value.typecode] and value.tobytes()==other.tobytes(),(tag,typ,other_type,str(value),str(other))
     old_type,new_type='B:'+value.typecode,'B:'+other.typecode
    mismatch.append(dict(read=a.query_name,tag=tag,source_type=old_type,cram_decode_type=new_type,value_bytes_equal=True))
 logical=('\n'.join(r.to_string() for r in records)+'\n').encode();oracle=p.with_suffix('.logical.sam');oracle.write_bytes(logical);b=p.read_bytes();stats=pysam.samtools.cram_size('-v',str(p));p.with_suffix('.size.txt').write_text(stats)
 inputs.append(dict(family='public-htslib-fixtures',stage=name,path=str(p),file=name,bytes=len(b),sha256=hashlib.sha256(b).hexdigest(),class_='PUBLIC_HTSLIB_FIXTURE_NOT_NATURAL_HISTORY',oracles=[dict(path=str(oracle),bytes=len(logical),sha256=hashlib.sha256(logical).hexdigest())],reads=len(records),native_semantic_signature=('KNOWN_JAVA_AUX_TYPE_MISMATCH' if mismatch else 'PASS') if sam else 'NATIVE_DECODE_ONLY_NO_ORIGINAL_SAM',known_semantic_mismatch=mismatch,reference=ref))
lib=ctypes.CDLL(sys.argv[2]);callback=ctypes.CFUNCTYPE(None,ctypes.c_int64,ctypes.c_int64,ctypes.c_int32,ctypes.c_void_p,ctypes.c_int,ctypes.c_int,ctypes.c_int);lib.scan_cram.argtypes=[ctypes.c_char_p,callback]
for r in inputs:
 p=pathlib.Path(r['path']);b=p.read_bytes();assert len(b)<=16<<20 and hashlib.sha256(b).hexdigest()==r['sha256'];blocks=[];payloads=[];details=[];errors=[]
 def consume(start,end,n,ptr,method,ctype,cid):
  tail=4 if b[4]>=3 else 0;off=end-tail-n;raw=ctypes.string_at(ptr,n)
  if not (0<=start<=off<=end<=len(b)) or raw!=b[off:off+n]:errors.append('native payload span')
  if tail and zlib.crc32(b[start:end-tail])!=int.from_bytes(b[end-tail:end],'little'):errors.append('block CRC')
  blocks.append([start,end-start])
  if n:payloads.append([off,n])
  details.append(dict(offset=start,bytes=end-start,payload_bytes=n,method=method,content_type=ctype,content_id=cid))
 status=lib.scan_cram(str(p).encode(),callback(consume));assert status==0 and not errors,(p,status,errors)
 r.update(resource_spans=blocks,chunk_spans=payloads,native_blocks=details,cram_version=list(b[4:6]))
(out/'inputs.json').write_text(json.dumps(inputs,indent=1));print(json.dumps(dict(inputs=len(inputs),blocks=sum(len(r['native_blocks']) for r in inputs),reads=sum(r['reads'] for r in inputs))))
