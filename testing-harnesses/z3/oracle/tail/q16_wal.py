"""Native SQLite producer/restore oracle. CACHE; public Chinook seed is pinned.
Captures committed append chains, checkpoint generations and independent replays.
These are producer workloads, not downloaded natural WAL histories.
"""
import hashlib,json,pathlib,shutil,sqlite3,sys
cache=pathlib.Path(sys.argv[1]);out=cache/'q16-corpus/wal';out.mkdir(parents=True,exist_ok=True);seed=cache/'q16-download/Chinook_Sqlite.sqlite';inputs=[]
def sha(b):return hashlib.sha256(b).hexdigest()
def logical(c):return sha(json.dumps(c.execute('select TrackId,Name,UnitPrice from Track order by TrackId').fetchall(),ensure_ascii=False,separators=(',',':')).encode())
def open_db(name):
 p=out/(name+'.sqlite');shutil.copyfile(seed,p);c=sqlite3.connect(p);assert c.execute('pragma journal_mode=wal').fetchone()[0]=='wal';c.execute('pragma wal_autocheckpoint=0');c.commit();return p,c
def capture(family,stage,p,c):
 target=out/(family+'-'+stage+'.wal');b=p.with_name(p.name+'-wal').read_bytes();assert len(b)<=128<<20;target.write_bytes(b);base=target.with_suffix('.base.sqlite');shutil.copyfile(p,base)
 # Reopen the captured base/WAL pair with native SQLite, independently of c.
 restore=out/(family+'-'+stage+'.restore.sqlite');shutil.copyfile(base,restore);restore.with_name(restore.name+'-wal').write_bytes(b)
 check=sqlite3.connect(restore);assert check.execute('pragma integrity_check').fetchone()==('ok',);assert logical(check)==logical(c);check.close()
 pagesize=int.from_bytes(b[8:12],'big');assert pagesize in [512,1024,2048,4096,8192,16384,32768,65536];assert (len(b)-32)%(24+pagesize)==0
 spans=[[32+i*(24+pagesize)+24,pagesize] for i in range((len(b)-32)//(24+pagesize))]
 inputs.append(dict(family=family,stage=stage,path=str(target),file=target.name,bytes=len(b),sha256=sha(b),class_='NATIVE_SQLITE_PRODUCER_NOT_NATURAL_WAL_HISTORY',resource_spans=[[s[0]-24,s[1]+24] for s in spans],chunk_spans=spans,oracles=[],native_restore=True,logical_sha256=logical(c),base_sha256=sha(base.read_bytes()),page_size=pagesize,frames=len(spans)))
p,c=open_db('append')
for n in range(5):
 c.execute('update Track set UnitPrice=UnitPrice+0.01 where TrackId=?',(n+1,));c.commit();capture('wal-append',str(n),p,c)
c.close()
p,c=open_db('checkpoint')
for n in range(4):
 c.execute('pragma wal_checkpoint(truncate)');c.execute('update Track set UnitPrice=UnitPrice+?',(0.01 if n%2==0 else -0.01,));c.commit();capture('wal-checkpoint',str(n),p,c)
c.close()
for n in range(3):
 p,c=open_db('independent'+str(n));c.execute('update Track set UnitPrice=UnitPrice+0.01');c.commit();capture('wal-independent',str(n),p,c);c.close()
(out/'inputs.json').write_text(json.dumps(inputs,indent=1));(out/'producer.json').write_text(json.dumps(dict(sqlite=sqlite3.sqlite_version,seed_sha256=sha(seed.read_bytes()),native_restore_checks=len(inputs)),indent=1));print(json.dumps(dict(inputs=len(inputs),frames=sum(r['frames'] for r in inputs))))
