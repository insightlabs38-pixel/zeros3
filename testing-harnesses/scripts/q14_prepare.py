"""Reuse the pinned bounded fetcher, then verify bounded native stream wrappers.
Usage: ROOT CACHE; native producer is separately invoked under unshare -Urnm.
The producer requires Q13's pinned libfuse2 dependency extraction.
"""
import bz2,hashlib,json,lzma,pathlib,subprocess,sys
root,cache=map(pathlib.Path,sys.argv[1:]);pins=root/'testing-harnesses/z3/testdata/q14-send';out=cache/'q14-corpus';out.mkdir(parents=True,exist_ok=True)
subprocess.run([sys.executable,str(root/'testing-harnesses/scripts/q11_fetch.py'),str(out),str(pins/'public.json')],check=True)
for r in json.loads((pins/'public.json').read_text()):
 p=out/r['decoded_file']
 if p.exists() and hashlib.sha256(p.read_bytes()).hexdigest()==r['decoded_sha256']:continue
 b=(out/r['file']).read_bytes();d=lzma.LZMADecompressor(memlimit=128<<20) if r['format']=='btrfs' else bz2.BZ2Decompressor();plain=d.decompress(b,max_length=(128<<20)+1)
 assert d.eof and not d.unused_data and len(plain)==r['decoded_bytes']<=128<<20 and hashlib.sha256(plain).hexdigest()==r['decoded_sha256'];p.write_bytes(plain)
(out/'public.json').write_bytes((pins/'public.json').read_bytes())
tools=cache/'q14-tools';tools.mkdir(exist_ok=True);manifest=tools/'fetch.json';manifest.write_text(json.dumps([json.loads((pins/'tools.json').read_text())]))
subprocess.run([sys.executable,str(root/'testing-harnesses/scripts/q11_fetch.py'),str(tools),str(manifest)],check=True)
if not (tools/'sbin/zfs-fuse').exists():subprocess.run(['dpkg-deb','-x',str(tools/json.loads((pins/'tools.json').read_text())['file']),str(tools)],check=True)
