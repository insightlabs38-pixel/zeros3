"""Real CLI old-generation refusal matrix. ROOT WORK. Keep raw outputs/digests."""
import hashlib,json,pathlib,shutil,subprocess,sys,gzip
root,w=map(pathlib.Path,sys.argv[1:3]);meta=json.loads((w/'run/seed.json').read_text());out=[];floor=int(sys.argv[sys.argv.index("--floor")+1]) if "--floor" in sys.argv else 9
def digest(p):return {str(f.relative_to(p)):hashlib.sha256(f.read_bytes()).hexdigest() for f in p.rglob('*') if f.is_file()}
ops=[['stats'],['verify'],['verify','-deep'],['gc'],['gc','-apply'],['compact'],['repack','-apply'],['tier','move','-from','hot','-to','warm','-all','-apply'],['doctor'],['doctor','-retire-bad-representations'],['versions','prune','-bucket','z3','-key','weights','-keep-last','0','-apply'],['bundle','export','-snapshot',meta['base'],'-out',str(w/'refused-full.zs3b')],['bundle','export','-snapshot',meta['target'],'-base-snapshot',meta['base'],'-out',str(w/'refused-delta.zs3d')],['bundle','import','-in',meta['full']],['bundle','import','-in',meta['delta']],['serve','-addr','127.0.0.1:0']]
for gen,binary in enumerate(meta['old_binaries']):
 for n,args in enumerate(ops):
  d=w/f'refusal-{gen}-{n}';shutil.copytree(meta['store'],d);before=digest(d);r=subprocess.run([binary,*args,'-store',str(d)],capture_output=True,text=True,timeout=15);after=digest(d);
  with gzip.open(w/f'refusal-{gen}-{n}-digests.json.gz','wt') as f:json.dump(dict(before=before,after=after),f,sort_keys=True)
  changed=[k for k in sorted(before.keys()|after.keys()) if before.get(k)!=after.get(k)]
  unsupported_cli='flag provided but not defined' in r.stderr
  row=dict(generation=gen,operation=args,exit=r.returncode,stdout=r.stdout,stderr=r.stderr,changed=changed,unsupported_cli=unsupported_cli);out.append(row)
  assert not [k for k in changed if k!='LOCK'],row
  if unsupported_cli:
   assert args==['doctor','-retire-bad-representations'] and gen<2,row
  else:assert r.returncode!=0 and f'unsupported store format version {floor}' in r.stderr,row
  shutil.rmtree(d) # verified clones are fully reproducible; evidence is retained
(w/'refusals.json').write_text(json.dumps(out,indent=1)+'\n');print('recorded',len(out),'operations;',sum(r['unsupported_cli'] for r in out),'unsupported old CLI cases, not counted as floor refusals')
