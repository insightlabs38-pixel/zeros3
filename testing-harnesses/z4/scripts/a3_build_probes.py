"""Build actual cumulative candidate and private uninstrumented/stage probes."""
import hashlib,json,pathlib,subprocess,sys
root,w,go=map(pathlib.Path,sys.argv[1:]);w.mkdir(exist_ok=True);s=(root/'zeros3.go').read_text();(w/'stock.go').write_text(s);subprocess.run([str(go),'build','-o',str(w/'zeros3'),str(w/'stock.go')],check=True)
probe=(w.parent/'z4-a0/i3/probe.go').read_text();helper=(root/'testing-harnesses/z4/probes/a3_stages.go.txt').read_text();raw=s.replace('func main() {','func privateProductMain() {',1)
records=[]
for profiled in [False,True]:
 d=w/('profiled' if profiled else 'plain');d.mkdir(exist_ok=True);(d/'production.go').write_text(raw)
 if profiled:
  command=[str(go),'-C',str(root/'testing-harnesses/z4'),'run','./cmd/phaseinject',str(d/'production.go'),str(d/'instrumented.go')];out=subprocess.check_output(command);(d/'declarations.json').write_bytes(out);(d/'production.go').write_bytes((d/'instrumented.go').read_bytes())
 p=probe
 if profiled:
  anchor='root, failure = auditEncode(b, f, st.Size(), kind)';assert p.count(anchor)==1;p=p.replace(anchor,'auditTotals=map[string]int64{};auditActive=true;defer func(){auditActive=false}();done:=auditEnter("other");defer done();'+anchor,1)
  anchor='"sink_put_ns": sink.NS';assert p.count(anchor)==1;p=p.replace(anchor,'"analysis_stages_ns":auditTotals, '+anchor,1)
  # CAS child spans override their caller's component/semantic stage.
  p=p.replace('func (a *auditSink) Put(id [32]byte, b []byte) error {','func (a *auditSink) Put(id [32]byte, b []byte) error {\n defer auditEnter("cas")()',1)
 (d/'probe.go').write_text(p+'\n'+helper)
 subprocess.run([str(go),'build','-o',str(d/'probe'),str(d/'production.go'),str(d/'probe.go')],check=True)
 records.append(dict(profiled=profiled,production_sha256=hashlib.sha256((d/'production.go').read_bytes()).hexdigest(),probe_sha256=hashlib.sha256((d/'probe.go').read_bytes()).hexdigest()))
(w/'build-provenance.json').write_text(json.dumps(dict(stock_sha256=hashlib.sha256(s.encode()).hexdigest(),copies=records),indent=1)+'\n')
