"""Verify Stage-A evidence and fail-closed source/AR bounds (CLI ROOT CACHE)."""
import gzip,hashlib,json,pathlib,subprocess,sys,tempfile
cli,root,cache=pathlib.Path(sys.argv[1]),pathlib.Path(sys.argv[2]),pathlib.Path(sys.argv[3])
def evidence(name,count):
    with gzip.open(root/'testing-harnesses/testdata/q11-xz'/name,'rt') as f:d=json.load(f)
    assert len(d['rows'])==count
    for r in d['rows']:
        assert bool(r['InteriorProofs']) and all(len(s)==64 for s in r['InteriorProofs'])
        assert r['OuterExact']==(r['Variant']=='compressed-cdc-prod')
        assert 0<=r['Incremental']<=r['Cumulative']
    return d
natural=evidence('characterization.json.gz',24);control=evidence('reexports.json.gz',8)
for row in json.loads((root/'testing-harnesses/testdata/q11-xz/public.json').read_text()):
    p=cache/row['file'];assert p.stat().st_size==row['bytes']<=16<<20
    assert hashlib.sha256(p.read_bytes()).hexdigest()==row['sha256']
checks=[]
with tempfile.TemporaryDirectory() as td:
    td=pathlib.Path(td);source=td/'source';manifest=td/'inputs.json'
    source.write_bytes(b'source changed')
    for label,size,reason in [('hash',14,'source hash'),('bound',(128<<20)+1,'source limit')]:
        if label=='bound':
            with source.open('wb') as f:f.truncate(size)
        manifest.write_text(json.dumps([dict(Family='negative',Path=str(source),SHA256='0'*64,File='negative')]))
        p=subprocess.run([str(cli),str(manifest)],capture_output=True,text=True,timeout=15)
        assert p.returncode!=0 and reason in p.stderr,(label,p.stderr)
        checks.append(label+' rejected')
    source=td/'bad.deb';header=bytearray(b' '*60);header[:8]=b'data.tar';header[48:58]=b'9999999999';header[58:]=b'`\n'
    data=b'!<arch>\n'+header;source.write_bytes(data)
    (td/'discovery.json').write_text(json.dumps([dict(file=source.name,sha256=hashlib.sha256(data).hexdigest())]))
    p=subprocess.run([sys.executable,str(root/'testing-harnesses/scripts/q11_prepare_xz.py'),str(td)],capture_output=True,text=True,timeout=15)
    assert p.returncode!=0 and 'ar span' in p.stderr,p.stderr
    checks.append('AR overflow/span rejected')
print(json.dumps(dict(natural_rows=24,control_rows=8,checks=checks)))
