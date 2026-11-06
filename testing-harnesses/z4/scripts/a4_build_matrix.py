"""Mechanically reuse A1's client matrix with native managed revision pairs."""
import pathlib,subprocess,sys
root,w,go=map(pathlib.Path,sys.argv[1:]);d=w/'harness';d.mkdir(exist_ok=True)
for name in ['go.mod','go.sum']:(d/name).write_bytes((root/'testing-harnesses'/name).read_bytes())
reuse=(root/'testing-harnesses/harness/z3_i0/main.go').read_text().replace('func main() {','func legacyHarnessMain() {',1);(d/'reuse.go').write_text(reuse)
s=(root/'testing-harnesses/z4/probes/a1_matrix.go.txt').read_text();old='data=append([]byte(nil),data...);data[len(data)-1]^=3;';assert s.count(old)==1;s=s.replace(old,'if path:=os.Getenv("Z4_TARGET_INPUT");path!=""{data,e=os.ReadFile(path);if e!=nil{panic(e)}}else{'+old+'};')
s=s.replace('formatLevel(dir)==7','formatLevel(dir)==9').replace('formatLevel(d)==7','formatLevel(d)==9').replace('raises format 7','raises format 9').replace('first Model publication','first managed publication')
s=s.replace('full,delta:=','verifyDeep("both native revisions represented",dir,2);full,delta:=',1)
(d/'matrix.go').write_text(s);subprocess.run([str(go),'-C',str(d),'build','-o',str(d/'run'),'.'],check=True)
