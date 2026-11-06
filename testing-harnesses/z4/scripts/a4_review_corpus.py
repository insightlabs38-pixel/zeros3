"""Mechanically reuse the independent CLR fixture producer; preselected density and overlay controls."""
import pathlib,subprocess,tempfile,json,hashlib,sys
root=pathlib.Path(sys.argv[1]);w=pathlib.Path(sys.argv[2]);go=sys.argv[3];pkg=root/'testing-harnesses/z3/clrz3'
with tempfile.TemporaryDirectory(prefix='a4-corpus-build-') as tmp:
 d=pathlib.Path(tmp)
 for p in [*pkg.glob('*.go')]:
  if not p.name.endswith('_test.go') or p.name=='builder_test.go':(d/p.name.replace('_test','')).write_text(p.read_text().replace('package clrz3','package main',1))
 (d/'producer_main.go').write_text('package main\nimport("os";"path/filepath")\nfunc main(){dir:=os.Args[1];for _,n:=range []int{128,512,1024}{b:=build(spec{types:n,methods:64,fields:8,fat:true});if e:=os.WriteFile(filepath.Join(dir,fmt.Sprintf("dense-%d.dll",n)),b,0600);e!=nil{panic(e)}}}\n')
 # fmt is per-file in Go; keep imports explicit.
 p=d/'producer_main.go';p.write_text(p.read_text().replace('"os";','"os";"fmt";'))
 subprocess.run([go,'build','-o',str(w/'corpus-producer'),*[str(p) for p in d.glob('*.go')]],cwd=root/'testing-harnesses/z3',check=True)
 subprocess.run([str(w/'corpus-producer'),str(w)],check=True)
b=(pathlib.Path('/workspace/work/q6-public/clr/13.0.4.dll')).read_bytes();(w/'near-32m.dll').write_bytes(b+bytes((32<<20)-4096-len(b)))
rows=[]
for p in sorted(w.glob('*.dll')):rows.append(dict(kind='clr',label=p.stem,path=str(p),bytes=p.stat().st_size,sha256=hashlib.sha256(p.read_bytes()).hexdigest()))
(w/'additional-inputs.json').write_text(json.dumps(rows,indent=1)+'\n');print(json.dumps(rows))
