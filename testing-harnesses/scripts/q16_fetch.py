"""Fetch bounded, hash-pinned public broad-tail artifacts. Usage: script CACHE MANIFEST"""
from pathlib import Path
from urllib.request import urlopen
import hashlib,io,json,subprocess,sys,zipfile
if len(sys.argv)!=3:raise SystemExit(__doc__)
root=Path(sys.argv[1]).resolve();root.mkdir(parents=True,exist_ok=True)
for row in json.loads(Path(sys.argv[2]).read_text()):
 out=root/row['file'];expected=row['sha256']
 if out.exists() and hashlib.sha256(out.read_bytes()).hexdigest()==expected:continue
 with urlopen(row['url'],timeout=30) as r:
  b=r.read((16<<20)+1)
 if len(b)>16<<20:raise RuntimeError('public artifact download bound')
 if hashlib.sha256(b).hexdigest()!=row.get('archive_sha256',expected):raise RuntimeError('public archive hash changed')
 if 'member' in row:
  with zipfile.ZipFile(io.BytesIO(b)) as z:
   info=z.getinfo(row['member'])
   if info.file_size>16<<20:raise RuntimeError('public member size bound')
   b=z.read(info)
 if hashlib.sha256(b).hexdigest()!=expected:raise RuntimeError('public artifact hash changed')
 out.write_bytes(b)
(root/'discovery.json').write_text(Path(sys.argv[2]).read_text())
print('public broad-tail artifacts verified')
