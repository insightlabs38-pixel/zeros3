"""Prepare pinned release sources using cached downloads and mechanical git archive.
Usage: ROOT CACHE. Q4 LLVM tools and the Q3 hiredis mirror are reused.
"""
import hashlib,io,json,pathlib,subprocess,sys,zipfile
root,cache=map(pathlib.Path,sys.argv[1:]);pins=root/'testing-harnesses/z3/testdata/q15-bitcode';out=cache/'q15-corpus/sqlite';out.mkdir(parents=True,exist_ok=True)
subprocess.run([sys.executable,str(root/'testing-harnesses/scripts/q11_fetch.py'),str(out),str(pins/'sqlite-pins.json')],check=True)
for r in json.loads((pins/'sqlite-pins.json').read_text()):
 with zipfile.ZipFile(io.BytesIO((out/r['file']).read_bytes())) as z:
  for name,want in r['members'].items():
   info=z.getinfo(f"sqlite-amalgamation-{r['version']}/{name}");assert info.file_size==want['bytes']<=16<<20;b=z.read(info);assert hashlib.sha256(b).hexdigest()==want['sha256'];d=out/str(r['version']);d.mkdir(exist_ok=True);(d/name).write_bytes(b)
mirror=cache/'i1c-hiredis.git'
for r in json.loads((pins/'hiredis-pins.json').read_text()):
 commit=subprocess.check_output(['git',f'--git-dir={mirror}','rev-parse',r['tag']],text=True).strip();assert commit==r['commit']
 out=cache/'q15-corpus/hiredis'/r['tag'];out.mkdir(parents=True,exist_ok=True);archive=out.parent/(r['tag']+'.tar')
 with archive.open('wb') as f:subprocess.run(['git',f'--git-dir={mirror}','archive',commit],stdout=f,check=True)
 assert archive.stat().st_size<=16<<20;subprocess.run(['tar','-xf',str(archive),'-C',str(out)],check=True)
print('cached release pins and mechanically extracted sources verified')
