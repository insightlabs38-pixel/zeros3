"""Verify final native identities and summarize serial medians. ROOT WORK OUTPUT."""
import gzip,hashlib,json,pathlib,statistics,sys
root,w,out=map(pathlib.Path,sys.argv[1:4]);source=hashlib.sha256((root/'zeros3.go').read_bytes()).hexdigest()
with gzip.open(root/'testing-harnesses/z4/a4-stop-evidence.json.gz','rt') as f:old=json.load(f)
expected={r['label']:r['root'] for r in old['method']['rows']}
for p in (w/'baseline-density').glob('*/results.jsonl'):
 r=json.loads(p.read_text());expected[pathlib.Path(r['file']).stem]=r['root']
shared=json.loads((w/'shared-8192-after.jsonl').read_text());expected['shared-dense']=shared['root']
summary={'production_sha256':source,'method':'Three fresh processes/stores per input; warm/unflushed OS cache, GC before encoding; compiler/producer excluded; CDC manifest ReaderAt; no test/build overlap. Separate one-trial 10ms temp monitor is not a latency claim. Existing roots unchanged; new shared-body root deterministic.'}
for name,folder,count in [('preceding_checkpoint','baseline-recheck',3),('final','integration-measure',3),('temporary_disk','integration-temp',1)]:
 grouped={}
 for d in sorted((w/folder).glob('case-*')):
  r=json.loads((d/'results.jsonl').read_text());resource=json.loads((d/'resource.json').read_text());label=resource['input']['label']
  assert resource['exit']==0 and r['exact'] and r['store_floor']==9 and r['sha256']==resource['input']['sha256']
  assert r['root']==expected[label],(label,r['root'],expected[label])
  assert r['production_sha256']==('64e997ef515c1406f328b2a00517180eca3dcf01287deb0d7dc390b05f45621a' if name=='preceding_checkpoint' else source)
  grouped.setdefault(label,[]).append((r,resource))
 rows=[]
 for label,rs in grouped.items():
  assert len(rs)==count
  median=lambda fn:statistics.median(fn(r,x) for r,x in rs)
  phase=lambda p:median(lambda r,x:r['phases'][p]['NS']/1e9)
  creation=['encode_with_cas','finish_root','flush','proof','reachability','outer_root_serialize','publication']
  row=dict(label=label,samples=count,bytes=rs[0][0]['bytes'],sha256=rs[0][0]['sha256'],root=rs[0][0]['root'],encode_s=phase('encode_with_cas'),proof_s=phase('proof'),cold_s=phase('decode_cold'),encode_rss_mib=median(lambda r,x:r['phases']['encode_with_cas']['RSSPeak']/2**20),creation_rss_mib=median(lambda r,x:max(r['phases'][p]['RSSPeak'] for p in creation)/2**20),cold_rss_mib=median(lambda r,x:r['phases']['decode_cold']['RSSPeak']/2**20),encode_heap_mib=median(lambda r,x:r['phases']['encode_with_cas']['HeapPeak']/2**20),creation_heap_mib=median(lambda r,x:max(r['phases'][p]['HeapPeak'] for p in creation)/2**20),process_maxrss_mib=median(lambda r,x:x['maxrss_kib']/1024),managed_tmp_mib=median(lambda r,x:x['managed_tmp_peak_bytes']/2**20),all_tmp_mib=median(lambda r,x:x['tmp_peak_bytes']/2**20),inspected=rs[0][0]['analysis_inspected_bytes'],additive_bytes=rs[0][0]['disk_after_rep']-rs[0][0]['disk_before_rep'])
  rows.append(row)
 assert len(rows)==(3 if name=='preceding_checkpoint' else 12)
 summary[name]=rows
out.write_text(json.dumps(summary,indent=1)+'\n')
for row in summary['final']:print(row['label'],round(row['encode_rss_mib'],2),round(row['creation_rss_mib'],2),*[round(row[k],3) for k in ['encode_s','proof_s','cold_s']])
