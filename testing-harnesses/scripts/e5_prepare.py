"""Verify cached HTSlib assets and compile an isolated native framing oracle.
Usage: ROOT CACHE; install pinned PySAM 0.23.3 into CACHE/e5-python first.
"""
import hashlib,json,pathlib,subprocess,sys,tarfile
root,cache=map(pathlib.Path,sys.argv[1:]);pins=root/'testing-harnesses/z3/testdata/e5-cram';download=cache/'e5-download';download.mkdir(parents=True,exist_ok=True);archive=json.loads((pins/'archive-pin.json').read_text());manifest=download/'fetch.json';manifest.write_text(json.dumps([archive]))
subprocess.run([sys.executable,str(root/'testing-harnesses/scripts/q11_fetch.py'),str(download),str(manifest)],check=True);out=cache/'e5-corpus/public';out.mkdir(parents=True,exist_ok=True)
with tarfile.open(download/archive['file']) as t:
 assert sum(m.size for m in t.getmembers())<=128<<20
 for r in json.loads((pins/'member-pins.json').read_text()):
  m=t.getmember(r['member']);assert m.isreg() and m.size==r['bytes']<=16<<20;b=t.extractfile(m).read();assert hashlib.sha256(b).hexdigest()==r['sha256'];(out/r['file']).write_bytes(b)
lib=next((cache/'e5-python/pysam').glob('libchtslib*.so'));subprocess.run(['cc','-shared','-fPIC','-O2','-I'+str(cache/'e5-python/pysam/include/htslib'),str(root/'testing-harnesses/z3/oracle/cram/e5_scan.c'),str(lib),'-Wl,-rpath,'+str(lib.parent),'-o',str(cache/'e5-scan.so')],check=True)
print('pinned reference corpus and isolated native block oracle verified')
