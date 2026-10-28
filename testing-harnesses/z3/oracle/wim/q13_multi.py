"""Native multi-image append/soft-delete controls. CACHE TOOL."""
import hashlib,json,os,pathlib,shutil,struct,subprocess,sys
root,tool=map(pathlib.Path,sys.argv[1:3]);runtime=tool.parents[2];env=dict(os.environ,LD_LIBRARY_PATH=str(runtime/'usr/lib/x86_64-linux-gnu')+':'+str(runtime/'lib/x86_64-linux-gnu'));rows=json.loads((root/'inputs.json').read_text());done={(r['family'],r['stage']) for r in rows}
for profile in ['none','lzx','solid-lzms']:
 ext='.esd' if profile=='solid-lzms' else '.wim'
 for stage in ['m0-base','m1-append','m2-soft-delete']:
  family='multi-'+profile
  if (family,stage) in done:continue
  p=root/family/(stage+ext);p.parent.mkdir(exist_ok=True)
  if p.exists():p.rename(pathlib.Path(str(p)+'.interrupted-'+str(os.getpid())))
  if stage=='m0-base':shutil.copyfile(root/profile/('r0'+ext),p);images=['r0']
  elif stage=='m1-append':
   shutil.copyfile(root/family/('m0-base'+ext),p)
   args=[str(tool),'append',str(root/'trees/r3'),str(p),'--threads=1','--unix-data','--check']
   if profile=='solid-lzms':args+=['--solid','--solid-compress=LZMS:10','--solid-chunk-size=64M']
   subprocess.run(args,env=env,check=True,stdout=open(str(p)+'.append.log','w'),stderr=subprocess.STDOUT,timeout=180);images=['r0','r3']
  else:
   shutil.copyfile(root/family/('m1-append'+ext),p);subprocess.run([str(tool),'delete',str(p),'1','--soft','--check'],env=env,check=True,stdout=open(str(p)+'.delete.log','w'),stderr=subprocess.STDOUT,timeout=180);images=['r3']
  subprocess.run([str(tool),'verify',str(p)],env=env,check=True,stdout=open(str(p)+'.verify.log','w'),stderr=subprocess.STDOUT,timeout=180)
  pathlib.Path(str(p)+'.lookup.txt').write_text(subprocess.check_output([str(tool),'info',str(p),'--lookup-table'],env=env,text=True))
  b=p.read_bytes();assert len(b)<=16<<20 and struct.unpack_from('<I',b,44)[0]==len(images)
  files=[];oracles=[];seen=set()
  for rev in images:
   original=next(r for r in rows if r['family']==profile and r['stage']==rev)
   for f in original['source_tree']:
    files.append(dict(f,path=rev+'/'+f['path']))
    if f['sha256'] not in seen:seen.add(f['sha256']);oracles.append(dict(path=str(root/'trees'/rev/f['path']),sha256=f['sha256'],bytes=f['bytes']))
  rows.append(dict(path=str(p),family=family,stage=stage,bytes=len(b),sha256=hashlib.sha256(b).hexdigest(),class_='PRODUCER_WORKLOAD_NOT_WINDOWS_HISTORY',source_tree=files,source_oracles=oracles,active_images=images,native_verified=True))
  (root/'inputs.json').write_text(json.dumps(rows,indent=1));print(family,stage,len(b),flush=True)
