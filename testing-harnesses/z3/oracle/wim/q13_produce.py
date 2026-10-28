"""Native WIM/solid-ESD controls; no Windows OS history is claimed."""
import os,pathlib,hashlib,json,shutil,subprocess,random,sys
root,tool,pe=map(pathlib.Path,sys.argv[1:4]);root.mkdir(parents=True,exist_ok=True)
runtime=tool.parents[2];env=dict(os.environ,LD_LIBRARY_PATH=str(runtime/'usr/lib/x86_64-linux-gnu')+':'+str(runtime/'lib/x86_64-linux-gnu'))
rows=json.loads((root/'inputs.json').read_text()) if (root/'inputs.json').exists() else []
completed={(r['family'],r['stage']):r for r in rows}
provenance=json.loads((pe.parents[1]/'windows-amd64-provenance.json').read_text())
for rev in ['r0','r1']:
 expected=next(r for r in provenance['artifacts'] if r['Flags']=='strip' and r['Rev']==rev)
 data=(pe/(rev+'.exe')).read_bytes();assert len(data)==expected['Size'] and hashlib.sha256(data).hexdigest()==expected['SHA256']
rng=random.Random(1041);blocks=[rng.randbytes(65536) for _ in range(8)];extra=rng.randbytes(65536)
for rev in range(6):
 tree=root/'trees'/('r'+str(rev));(tree/'Windows/System32').mkdir(parents=True,exist_ok=True);(tree/'data').mkdir(exist_ok=True)
 shutil.copyfile(pe/('r0.exe' if rev==0 else 'r1.exe'),tree/'Windows/System32/app.exe')
 if not (tree/'Windows/System32/app-alias.exe').exists():os.link(tree/'Windows/System32/app.exe',tree/'Windows/System32/app-alias.exe')
 assert os.stat(tree/'Windows/System32/app.exe').st_ino==os.stat(tree/'Windows/System32/app-alias.exe').st_ino
 for i,b in enumerate(blocks):
  if rev==5 and i==2:continue
  name='renamed.bin' if rev==5 and i==3 else f'block-{i}.bin'
  if i==0 and rev>=2:b=b[:1000]+b'changed-native-cell'+b[1019:]
  (tree/'data'/name).write_bytes(b)
 if rev>=3:(tree/'data/appended.bin').write_bytes(extra)
 files=[]
 for p in sorted(tree.rglob('*'),reverse=True):
  os.chmod(p,0o755 if p.is_dir() else 0o644);os.utime(p,(1700000060 if rev==4 else 1700000000,)*2)
  if p.is_file():
   b=p.read_bytes();files.append(dict(path=str(p.relative_to(tree)),bytes=len(b),sha1=hashlib.sha1(b).hexdigest(),sha256=hashlib.sha256(b).hexdigest()))
 os.utime(tree,(1700000060 if rev==4 else 1700000000,)*2)
 for profile,compression,solid in [('none','none',False),('xpress','XPRESS:20',False),('lzx','LZX:20',False),('solid-lzms','LZMS:10',True)]:
  out=root/profile/('r'+str(rev)+('.esd' if solid else '.wim'));out.parent.mkdir(exist_ok=True)
  key=(profile,'r'+str(rev))
  if key in completed:
   assert out.stat().st_size==completed[key]['bytes'] and hashlib.sha256(out.read_bytes()).hexdigest()==completed[key]['sha256'];continue
  if out.exists():out.rename(pathlib.Path(str(out)+'.interrupted-'+str(os.getpid())))
  args=[str(tool),'capture',str(tree),str(out),'--compress='+compression,'--threads=1','--unix-data','--check']
  if solid:args+=['--solid','--solid-compress=LZMS:10','--solid-chunk-size=64M']
  subprocess.run(args,env=env,check=True,stdout=open(str(out)+'.capture.log','w'),stderr=subprocess.STDOUT,timeout=180)
  subprocess.run([str(tool),'verify',str(out)],env=env,check=True,stdout=open(str(out)+'.verify.log','w'),stderr=subprocess.STDOUT,timeout=180)
  info=subprocess.check_output([str(tool),'info',str(out),'--lookup-table'],env=env,text=True);pathlib.Path(str(out)+'.lookup.txt').write_text(info)
  b=out.read_bytes();assert len(b)<=16<<20
  rows.append(dict(path=str(out),family=profile,stage='r'+str(rev),bytes=len(b),sha256=hashlib.sha256(b).hexdigest(),class_='PRODUCER_WORKLOAD_NOT_WINDOWS_HISTORY',source_tree=files,options=args[4:],native_verified=True))
  (root/'inputs.json').write_text(json.dumps(rows,indent=1));print(profile,rev,len(b),flush=True)
(root/'tool-version.txt').write_text(subprocess.check_output([str(tool),'--version'],env=env,text=True))
