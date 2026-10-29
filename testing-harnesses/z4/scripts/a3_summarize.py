"""Summarize complete final phase evidence, without substituting contended runs."""
import hashlib,json,pathlib,statistics,sys
root,w,out=map(pathlib.Path,sys.argv[1:]);out.mkdir(exist_ok=True);records=[]
for d in sorted(w.glob('phase-*')):
 resource=json.loads((d/'resource.json').read_text());rows=[json.loads(s) for s in (d/'results.jsonl').read_text().splitlines()];assert resource['exit']==0 and len(rows)==1 and rows[0].get('exact');r=rows[0];assert r['bytes']==resource['input']['bytes'] and r['sha256']==resource['input']['sha256'];records.append((resource,r))
summary=[]
for path in sorted(set(a['input']['path'] for a,r in records)):
 rows=[r for a,r in records if a['input']['path']==path and a['mode']=='plain'];profile=[r for a,r in records if a['input']['path']==path and a['mode']=='profiled'];assert rows and len(profile)==1
 phase={k:dict(median_seconds=statistics.median(r['phases'][k]['NS']/1e9 for r in rows),max_sampled_incremental_rss_mib=max((r['phases'][k]['RSSPeak']-r['phases'][k]['RSSBefore'])/2**20 for r in rows),max_sampled_heap_mib=max(r['phases'][k]['HeapPeak']/2**20 for r in rows)) for k in rows[0]['phases']}
 stages={k:v/1e9 for k,v in profile[0]['analysis_stages_ns'].items()}
 summary.append(dict(path=path,bytes=rows[0]['bytes'],samples=len(rows),phases=phase,profiled_exclusive_stage_seconds=stages,max_process_rss_mib=max(a['maxrss_kib']/1024 for a,r in records if a['input']['path']==path and a['mode']=='plain'),optional_disk_growth_bytes=[r['disk_after_rep']-r['disk_before_rep'] for r in rows]))
provenance=json.loads((w/'build-provenance.json').read_text());assert provenance['stock_sha256']==hashlib.sha256((root/'zeros3.go').read_bytes()).hexdigest()
(out/'phase-summary.json').write_text(json.dumps(summary,indent=1)+'\n')
for r in summary:
 p=r['phases'];encode=p['encode_with_cas']['median_seconds'];print(r['path'],*[round(p[k]['median_seconds'],4) for k in ['encode_with_cas','proof','decode_cold']],round(r['bytes']/2**20/encode,2),round(r['max_process_rss_mib'],2))
