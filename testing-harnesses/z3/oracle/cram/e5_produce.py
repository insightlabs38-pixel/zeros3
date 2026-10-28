"""Native CRAM producer and full alignment-field oracle, using public HTSlib SAM.
Usage: CACHE. These scaled edits are controlled workloads, not natural histories.
"""
import hashlib,json,pathlib,sys,pysam
cache=pathlib.Path(sys.argv[1]);out=cache/'e5-corpus/native';out.mkdir(parents=True,exist_ok=True);public=cache/'e5-corpus/public';ref=public/'ce.fa';pysam.faidx(str(ref))
with pysam.AlignmentFile(str(public/'ce#1000.sam'),'r') as f:header=f.header.to_dict();source=[r.to_string() for r in f]
assert len(source)==1000
header['HD']={'VN':'1.6','SO':'coordinate'};h=pysam.AlignmentHeader.from_dict(header);base=[]
for generation in range(32):
 for text in source:
  r=pysam.AlignedSegment.fromstring(text,h);r.query_name=f'g{generation}_{r.query_name}'
  for tag in ['MD','NM']:
   if r.has_tag(tag):r.set_tag(tag,None)
  base.append(r.to_string())
base.sort(key=lambda s:(pysam.AlignedSegment.fromstring(s,h).reference_id,pysam.AlignedSegment.fromstring(s,h).reference_start,s.split('\t')[0]))
def signature(r):
 # MD/NM are reference-derived, not a promise of original textual SAM encoding.
 return (r.query_name,r.flag,r.reference_id,r.reference_start,r.mapping_quality,r.cigarstring,r.next_reference_id,r.next_reference_start,r.template_length,r.query_sequence,list(r.query_qualities or []),sorted((k,str(v)) for k,v in r.get_tags() if k not in ['MD','NM']))
inputs=[]
for profile,options in [('cram30',[b'version=3.0',b'level=5']),('cram31',[b'version=3.1',b'level=5']),('cram31-noref',[b'version=3.1',b'level=5',b'no_ref=1'])]:
 for stage in ['base','base-cell','quality-cell','append','metadata','slice-repack']:
  hd=dict(header);records=[pysam.AlignedSegment.fromstring(text,h) for text in base];opts=options+[b'seqs_per_slice='+ (b'2000' if stage=='slice-repack' else b'10000')]
  if stage=='base-cell':
   r=records[16000];quality=r.query_qualities;sequence=r.query_sequence;r.query_sequence=('A' if sequence[0]!='A' else 'C')+sequence[1:];r.query_qualities=quality
  if stage=='quality-cell':r=records[16000];q=r.query_qualities;q[0]=(q[0]+1)%40;r.query_qualities=q
  if stage=='append':
   for n,text in enumerate(base[-256:]):r=pysam.AlignedSegment.fromstring(text,h);r.query_name='append_'+r.query_name;records.append(r)
   records.sort(key=lambda r:(r.reference_id,r.reference_start,r.query_name))
  if stage=='metadata':hd['CO']=['native metadata-only controlled edit']
  p=out/f'{profile}-{stage}.cram'
  with pysam.AlignmentFile(str(p),'wc',header=hd,reference_filename=str(ref),format_options=opts) as f:
   for r in records:f.write(r)
  with pysam.AlignmentFile(str(p),'rc',reference_filename=str(ref)) as f:
   restored=list(f);assert len(restored)==len(records)
   for a,b in zip(records,restored):assert signature(a)==signature(b),(profile,stage,a.query_name)
   logical=('\n'.join(r.to_string() for r in restored)+'\n').encode();sam=p.with_suffix('.logical.sam');sam.write_bytes(logical)
  b=p.read_bytes();assert len(b)<=16<<20 and len(logical)<=16<<20;stats=pysam.samtools.cram_size('-v',str(p));p.with_suffix('.size.txt').write_text(stats)
  inputs.append(dict(family=profile,stage=stage,path=str(p),file=p.name,bytes=len(b),sha256=hashlib.sha256(b).hexdigest(),class_='SCALED_PUBLIC_SAM_NATIVE_EDIT_WORKLOAD_NOT_NATURAL_HISTORY',oracles=[dict(path=str(sam),bytes=len(logical),sha256=hashlib.sha256(logical).hexdigest())],reads=len(records),native_semantic_signature='PASS',native_stats_sha256=hashlib.sha256(stats.encode()).hexdigest()))
(out/'inputs.json').write_text(json.dumps(inputs,indent=1));(out/'versions.json').write_text(json.dumps(dict(pysam=pysam.__version__,samtools=pysam.__samtools_version__,source_sam_sha256=hashlib.sha256((public/'ce#1000.sam').read_bytes()).hexdigest(),reference_sha256=hashlib.sha256(ref.read_bytes()).hexdigest(),reads_base=32000),indent=1));print(json.dumps(dict(inputs=len(inputs),reads=sum(r['reads'] for r in inputs))))
