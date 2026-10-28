"""Validate all draft evidence and archive immutable measurements. ROOT WORK OUT."""
import gzip, hashlib, json, pathlib, sys
root, work, out = map(pathlib.Path, sys.argv[1:]); out.mkdir(parents=True, exist_ok=True)
cohorts = {}
for name, cache, count in [('model','q9-model-final-probe',3),('dex-before','q9-dex-probe',3),('dex-after','q9-dex-lean-probe',3),('clr-before','q9-clr-probe',5),('clr-after','q9-clr-lean-probe',5),('fsharp','q9-fsharp-final-probe',2),('managed-install','q9-managed-install-probe',2)]:
    directory=pathlib.Path('/workspace/work', cache)
    meta=json.load(open(directory/'resource.json')); rows=[json.loads(x) for x in open(directory/'results.jsonl')]
    assert meta['exit']==0 and len(rows)==count and all(r['Exact'] for r in rows)
    for item in meta['inputs']:
        data=pathlib.Path(item['path']).read_bytes()
        assert hashlib.sha256(data).hexdigest()==item['sha256'] and len(data)==item['bytes']
    cohorts[name]=dict(resource=meta,results=rows)
for kind in ['dex','clr']:
    a,b=cohorts[kind+'-before']['results'],cohorts[kind+'-after']['results']
    assert [(x['SHA'],x['Root']) for x in a]==[(x['SHA'],x['Root']) for x in b]
checks={}
for name,count in [('compat',54),('kinds',32),('bundles',24),('crash',24),('i2-compat',23),('managed-compat',23),('i2-delta',23)]:
    text=(work/(name+'.log')).read_text(); assert 'FAIL:' not in text and text.count('PASS:')==count
    checks[name]=count
for name in ['root-tests','root-vet','lab-tests','lab-vet','i2-race','fuzz','negative','base-gc','lean-fidelity']:
    text=(work/(name+'.log')).read_text(); assert 'FAIL' not in text
    if name not in ['root-vet','lab-vet']: assert 'ok ' in text or 'PASS' in text
memory=json.load(open(work/'gib-resource.json')); assert memory['exit']==0
production_sha=hashlib.sha256((root/'zeros3.go').read_bytes()).hexdigest()
assert memory['production_sha256']==production_sha
for name in ['model','dex-after','clr-after','fsharp','managed-install']:
    assert cohorts[name]['resource']['production_sha256']==production_sha
summary=dict(status='I2_DRAFT_BLOCKED', ordinary_managed_creation=False,
    blocker='whole-file masked managed analysis and high transient identity/reference memory; requires streaming ReaderAt/group emission before production creation',
    production_sha256=production_sha, production_before_lines=37850,
    production_after_lines=len((root/'zeros3.go').read_text().splitlines()),
    checks=checks,gib_memory=memory,cohorts=cohorts,
    public_provenance=json.load(open('/workspace/work/q6-public/provenance.json')),
    fsharp=json.load(open('/workspace/work/q9-fsharp/canonical-provenance.json')))
with gzip.GzipFile(filename=str(out/'validation.json.gz'), mode='wb', mtime=0) as f:
    f.write(json.dumps(summary,sort_keys=True,separators=(',',':')).encode())
for name in ['compat','kinds','bundles','crash','i2-compat','managed-compat','i2-delta','gib','root-tests','root-vet','lab-tests','lab-vet','i2-race','fuzz','base-gc']:
    with gzip.GzipFile(filename=str(out/(name+'.log.gz')),mode='wb',mtime=0) as f:
        f.write((work/(name+'.log')).read_bytes())
print(json.dumps(dict(checks=checks,gib_memory=memory,production_sha256=production_sha),indent=2))
