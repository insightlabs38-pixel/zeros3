"""Native RocksDB SST memory controls. Options mechanically reuse Q6/Q8 driver.
PATH MiB CODEC; streamed unique keys/values, no retained whole-corpus array.
"""
import hashlib,json,pathlib,sys,time
import rocksdict
path=pathlib.Path(sys.argv[1]);mib=int(sys.argv[2]);codec=sys.argv[3]
FILE,BLOCK,scale=4<<20,4096,1.0
r = rocksdict
o = r.Options(raw_mode=True); o.create_if_missing(True)
o.set_write_buffer_size(32 << 20); o.set_target_file_size_base(int(FILE * scale))
o.set_disable_auto_compactions(True); o.set_compression_type(getattr(r.DBCompressionType, codec)())
o.set_max_background_jobs(1)
bb = r.BlockBasedOptions(); bb.set_block_size(BLOCK); bb.set_bloom_filter(10.0, False)
o.set_block_based_table_factory(bb)

writer=r.SstFileWriter(o);writer.open(str(path));rows=(mib<<20)//1024
for i in range(rows):
 key=i.to_bytes(8,"big");value=hashlib.sha256(b"z4-native-memory"+key).digest()*32;writer[key]=value
writer.finish();h=hashlib.sha256()
with path.open("rb") as f:
 while b:=f.read(1<<20):h.update(b)
meta=dict(producer="RocksDB",version="9.8.4",binding="rocksdict 0.3.29",target_value_mib=mib,rows=rows,codec=codec,bytes=path.stat().st_size,sha256=h.hexdigest(),class_="NATIVE_SYNTHETIC_MEMORY_ADMISSION_CONTROL")
path.with_suffix(path.suffix+".json").write_text(json.dumps(meta,indent=1)+"\n");print(json.dumps(meta))
