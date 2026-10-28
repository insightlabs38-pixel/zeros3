"""Build the actual clean production candidate and reuse the A0 phase probe."""
import pathlib,subprocess,sys,hashlib,json
root,w,go=map(pathlib.Path,sys.argv[1:]);d=w/'model';d.mkdir(parents=True,exist_ok=True);s=(root/'zeros3.go').read_text();(d/'stock.go').write_text(s);(d/'production.go').write_text(s.replace('func main() {','func privateProductMain() {',1))
probe=(w.parent/'z4-a0/i2/probe.go').read_text();a=probe.index('\tcase "dex", "clr":');b=probe.index('\tdefault:',a);probe=probe[:a]+probe[b:];(d/'probe.go').write_text(probe)
for name,files in [('zeros3',['stock.go']),('probe',['production.go','probe.go'])]:subprocess.run([str(go),'build','-o',str(d/name),*[str(d/f) for f in files]],check=True)
(w/'probe-build.json').write_text(json.dumps(dict(production_sha256=hashlib.sha256(s.encode()).hexdigest(),probe_sha256=hashlib.sha256(probe.encode()).hexdigest()),indent=1)+'\n')
