"""Summarize completed, immutable phase runs; preserve missing recorder status."""
import gzip,json,pathlib,statistics,sys
w,out=map(pathlib.Path,sys.argv[1:]);out.mkdir(parents=True,exist_ok=True)
resources=[];cases=[]
for d in sorted((w/'phase-v2').iterdir()):
 resource=json.loads((d/'resource.json').read_text());resources.append(resource)
 for line in (d/'results.jsonl').read_text().splitlines():
  r=json.loads(line);assert r.get('exact'),r;cases.append(dict(cohort=d.name,**r))
groups={}
for r in cases:groups.setdefault((r['kind'],r['file']),[]).append(r)
summary=[]
for (kind,file),rows in groups.items():
 phases={}
 for phase in rows[0]['phases']:
  values=[r['phases'][phase] for r in rows]
  phases[phase]=dict(median_seconds=statistics.median(v['NS']/1e9 for v in values),max_sampled_incremental_rss_mib=max((v['RSSPeak']-v['RSSBefore'])/2**20 for v in values),max_sampled_heap_mib=max(v['HeapPeak']/2**20 for v in values))
 summary.append(dict(kind=kind,file=file,bytes=rows[0]['bytes'],samples=len(rows),phases=phases,median_sink_seconds=statistics.median(r['sink_put_ns']/1e9 for r in rows),optional_disk_delta_bytes=[r['disk_after_rep']-r['disk_before_rep'] for r in rows]))
raw=dict(cases=cases,resources=resources,index=[json.loads(s) for s in (w/'index.jsonl').read_text().splitlines()],old_operations=json.loads((w/'old-operations.json').read_text()),other_kinds=json.loads((w/'remaining-kinds.json').read_text()),build=json.loads((w/'build-provenance.json').read_text()))
with gzip.GzipFile(filename=str(out/'measurements.json.gz'),mode='wb',mtime=0) as f:f.write(json.dumps(raw,sort_keys=True).encode())
(out/'phase-summary.json').write_text(json.dumps(summary,indent=1)+'\n')
for r in summary:
 p=r['phases'];print(r['kind'],pathlib.Path(r['file']).name,r['samples'],*[round(p[x]['median_seconds'],4) for x in ['encode_with_cas','proof','decode_cold']],round(p['encode_with_cas']['max_sampled_incremental_rss_mib'],2))
