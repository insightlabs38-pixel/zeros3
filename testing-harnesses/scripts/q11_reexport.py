import hashlib,json,pathlib,subprocess,sys
root=pathlib.Path(sys.argv[1]);tar=root/'coreutils-9.7.tar';rows=[]
for level in [0,6]:
 p=root/('reexport-'+str(level)+'.tar.xz')
 if not p.exists():
  with open(p,'wb') as f:subprocess.run(['xz','-'+str(level),'-c',str(tar)],stdout=f,check=True,timeout=180)
 if p.stat().st_size>16<<20:raise ValueError('compressed artifact bound')
 b=p.read_bytes();rows.append(dict(family='same-input-reexport-control',path=str(p),file=p.name,sha256=hashlib.sha256(b).hexdigest(),bytes=len(b),class_='REAL_REEXPORT_NOT_HISTORY',oracles=[dict(path=str(tar),sha256=hashlib.sha256(tar.read_bytes()).hexdigest(),bytes=tar.stat().st_size)]))
(root/'reexport-inputs.json').write_text(json.dumps(rows,indent=1))
print(subprocess.check_output(['xz','--version'],text=True).splitlines()[0]);print([(r['file'],r['bytes']) for r in rows])
