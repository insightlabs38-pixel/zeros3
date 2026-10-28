"""Native SQLite, OCI codec reexport and SST snapshots: script CACHE OCI_CACHE SST_CACHE."""
from pathlib import Path
import sqlite3,hashlib,json,random,gzip,sys,io,tarfile
import zstandard
root=Path(sys.argv[1]).resolve();oci=Path(sys.argv[2]);sst=Path(sys.argv[3]);root.mkdir(parents=True,exist_ok=True)
manifest=root/'provenance.json'
if manifest.exists():
 rows=json.loads(manifest.read_text())
 for r in rows:
  b=Path(r['file']).read_bytes()
  if len(b)!=r['bytes'] or hashlib.sha256(b).hexdigest()!=r['sha256']:raise RuntimeError('producer cache changed')
 print('verified producer cache');raise SystemExit(0)
rows=[]
def add(group,label,path,klass,producer,source=None):
 b=path.read_bytes();rows.append({'group':group,'label':label,'file':str(path),'class':klass,'producer':producer,'source':source,'bytes':len(b),'sha256':hashlib.sha256(b).hexdigest()})
build=root/'build.sqlite';db=sqlite3.connect(build);db.execute('PRAGMA page_size=4096');db.execute('CREATE TABLE records(id INTEGER PRIMARY KEY, payload BLOB, category TEXT)');db.execute('CREATE INDEX cat ON records(category)')
rng=random.Random(36);db.executemany('INSERT INTO records VALUES(?,?,?)',[(i,rng.randbytes(512),'group'+str(i%17)) for i in range(1800)]);db.commit()
for rev in range(4):
 if rev in [1,2]:db.executemany('UPDATE records SET payload=? WHERE id=?',[(rng.randbytes(512),i) for i in range(7*rev,1800,193)]);db.commit()
 if rev==3:db.execute('VACUUM')
 out=root/f'sqlite-{rev}.db';dest=sqlite3.connect(out);db.backup(dest);assert dest.execute('PRAGMA integrity_check').fetchone()==('ok',);dest.close();add('sqlite-native',str(rev),out,'PRODUCER_WORKLOAD','SQLite '+sqlite3.sqlite_version)
db.close()
for image in json.loads((oci/'REAL_REGISTRY.json').read_text()):
 for layer in image['layers']:
  src=oci/'blobs'/layer['digest'].split(':')[1];raw=src.read_bytes();assert len(raw)==layer['size'] and 'sha256:'+hashlib.sha256(raw).hexdigest()==layer['digest'];plain=gzip.decompress(raw);assert 'sha256:'+hashlib.sha256(plain).hexdigest()==layer['diff_id']
  add('oci-gzip',image['tag'],src,'REAL','registry',layer)
  out=root/('oci-'+image['tag']+'.zst');out.write_bytes(zstandard.ZstdCompressor(level=3).compress(plain));add('oci-zstd',image['tag'],out,'REAL_REEXPORT','zstandard '+zstandard.__version__,layer)
for engine in ['rocksdb','pebble']:
 for directory in sorted((sst/engine/'chain').glob('c[0-9]*')):
  out=root/(engine+'-'+directory.name+'.tar')
  with tarfile.open(out,'w',format=tarfile.USTAR_FORMAT) as tar:
   for file in sorted(directory.glob('*.sst')):
    b=file.read_bytes();h=tarfile.TarInfo(file.name);h.size=len(b);h.mode=0o644;tar.addfile(h,io.BytesIO(b))
  add(engine,directory.name,out,'PRODUCER_WORKLOAD',engine+' existing Z3-30 driver; 1/100 base record counts; chain operations unchanged')
manifest.write_text(json.dumps(rows,indent=2)+'\n');print('pinned',len(rows),'producer inputs')
