"""Mechanically reuse the independent CLR fixture producer; preselected density and overlay controls."""
import pathlib,subprocess,tempfile,json,hashlib,sys
root=pathlib.Path(sys.argv[1]);w=pathlib.Path(sys.argv[2]);go=sys.argv[3];pkg=root/'testing-harnesses/z3/clrz3'
with tempfile.TemporaryDirectory(prefix='a4-corpus-build-') as tmp:
 d=pathlib.Path(tmp)
 for p in [*pkg.glob('*.go')]:
  if not p.name.endswith('_test.go') or p.name=='builder_test.go':(d/p.name.replace('_test','')).write_text(p.read_text().replace('package clrz3','package main',1))
 (d/'producer_main.go').write_text(r'''package main
import("os";"fmt";"path/filepath")
func sharedDense() []byte{
 a:=newAsm();a.fat=true
 a.add(TModule,0,a.s("shared.dll"),1,0,0)
 a.add(TTypeDef,0,a.s("<Module>"),0,0,1,1)
 a.add(TTypeDef,0x100001,a.s("Shared"),0,0,1,1)
 sig:=a.b([]byte{0,0,1})
 for i:=0;i<64;i++{a.add(TMethodDef,0,0,0x16,a.s(fmt.Sprintf("M%d",i)),sig,1)}
 var body []byte;for i:=0;i<65536;i++{body=append(body,il(byte(0x28),uint32(0x06000001))...)};body=append(body,0x2a);a.bodies=[][]byte{body}
 b:=a.image();f,e:=Parse(bytes.NewReader(b),int64(len(b)),Limits{});if e!=nil{panic(e)}
 rva:=f.Col(b,TMethodDef,1,0)
 for r:=uint32(2);r<=64;r++{binary.LittleEndian.PutUint32(b[f.RowOff(TMethodDef,r):],rva)}
 return b
}
func main(){dir:=os.Args[1];for _,n:=range []int{128,512,1024}{b:=build(spec{types:n,methods:64,fields:8,fat:true});if e:=os.WriteFile(filepath.Join(dir,fmt.Sprintf("dense-%d.dll",n)),b,0600);e!=nil{panic(e)}};if e:=os.WriteFile(filepath.Join(dir,"shared-dense.dll"),sharedDense(),0600);e!=nil{panic(e)}}
''')
 p=d/'producer_main.go';p.write_text(p.read_text().replace('import("os";', 'import("bytes";"encoding/binary";"os";'))
 subprocess.run([go,'build','-o',str(w/'corpus-producer'),*[str(p) for p in d.glob('*.go')]],cwd=root/'testing-harnesses/z3',check=True)
 subprocess.run([str(w/'corpus-producer'),str(w)],check=True)
b=(pathlib.Path('/workspace/work/q6-public/clr/13.0.4.dll')).read_bytes();(w/'near-32m.dll').write_bytes(b+bytes((32<<20)-4096-len(b)))
rows=[]
for p in sorted(w.glob('*.dll')):rows.append(dict(kind='clr',label=p.stem,path=str(p),bytes=p.stat().st_size,sha256=hashlib.sha256(p.read_bytes()).hexdigest()))
(w/'additional-inputs.json').write_text(json.dumps(rows,indent=1)+'\n');print(json.dumps(rows))
