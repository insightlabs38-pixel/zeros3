"""Real CLI old-generation refusal matrix. ROOT WORK. Keep raw outputs/digests."""
import hashlib,json,pathlib,shutil,subprocess,sys
root,w=map(pathlib.Path,sys.argv[1:]);meta=json.loads((w/'run/seed.json').read_text());out=[]
def digest(p):return {str(f.relative_to(p)):hashlib.sha256(f.read_bytes()).hexdigest() for f in p.rglob('*') if f.is_file()}
ops=[['stats'],['verify'],['verify','-deep'],['gc'],['gc','-apply'],['compact'],['repack','-apply'],['tier','move','-from','hot','-to','warm','-all','-apply'],['doctor'],['doctor','-retire-bad-representations'],['versions','prune','-bucket','z3','-key','weights','-keep-last','0','-apply'],['bundle','export','-snapshot',meta['base'],'-out',str(w/'refused-full.zs3b')],['bundle','export','-snapshot',meta['target'],'-base-snapshot',meta['base'],'-out',str(w/'refused-delta.zs3d')],['bundle','import','-in',meta['full']],['bundle','import','-in',meta['delta']],['serve','-addr','127.0.0.1:0']]
for gen,binary in enumerate(meta['old_binaries']):
 for n,args in enumerate(ops):
  d=w/f'refusal-{gen}-{n}';shutil.copytree(meta['store'],d);before=digest(d);r=subprocess.run([binary,*args,'-store',str(d)],capture_output=True,text=True,timeout=15);after=digest(d);changed=[k for k in sorted(before.keys()|after.keys()) if before.get(k)!=after.get(k)]
  unsupported_cli='flag provided but not defined' in r.stderr
  row=dict(generation=gen,operation=args,exit=r.returncode,stdout=r.stdout,stderr=r.stderr,changed=changed,unsupported_cli=unsupported_cli);out.append(row)
  assert not [k for k in changed if k!='LOCK'],row
  if unsupported_cli:
   assert args==['doctor','-retire-bad-representations'] and gen<2,row
  else:assert r.returncode!=0 and 'unsupported store format version 7' in r.stderr,row
(w/'refusals.json').write_text(json.dumps(out,indent=1)+'\n');print('recorded',len(out),'operations;',sum(r['unsupported_cli'] for r in out),'unsupported old CLI cases, not counted as floor refusals')
# Each later tranche publishes through its actual patched draft decoder/proof.
inputs=json.loads((root/'testing-harnesses/z4/testdata/a0-audit/preselected-inputs.json').read_text())
for kind,name,floor in [('sst','structured',8),('parquet','structured',8),('dex','managed',9),('clr','managed',9)]:
 d=w/('later-'+kind);r=next(r for r in inputs if r['kind']==kind)
 with (w/(kind+'-publication.jsonl')).open('w') as f:subprocess.run([str(w/name/'probe'),kind+'@'+str(d),r['path']],stdout=f,stderr=subprocess.PIPE,check=True)
 fmt=json.loads((d/'FORMAT.json').read_text());assert fmt['store_format_version']==floor,fmt
 for earlier in ['model']+(['structured'] if floor==9 else []):
  before=digest(d);res=subprocess.run([str(w/earlier/'zeros3'),'doctor','-retire-bad-representations','-store',str(d)],capture_output=True,text=True);after=digest(d);changed=[k for k in sorted(before.keys()|after.keys()) if before.get(k)!=after.get(k)];row=dict(kind=kind,earlier=earlier,floor=floor,exit=res.returncode,stdout=res.stdout,stderr=res.stderr,changed=changed);out.append(row);assert res.returncode!=0 and f'unsupported store format version {floor}' in res.stderr,row;assert not [k for k in changed if k!='LOCK'],row
(w/'refusals.json').write_text(json.dumps(out,indent=1)+'\n')
