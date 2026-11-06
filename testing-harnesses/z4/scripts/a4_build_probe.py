"""Reuse A0's isolated production phase probe with actual managed store readers."""
import hashlib,json,pathlib,subprocess,sys
root,w=map(pathlib.Path,sys.argv[1:3]);go=sys.argv[3];w.mkdir(parents=True,exist_ok=True)
s=(root/'zeros3.go').read_text();assert s.count('func main() {')==1
(w/'production.go').write_text(s.replace('func main() {','func zeros3ManagedMain() {',1))
p=(root/'testing-harnesses/z4/probes/a0_phases.go.txt').read_text()
old='b:=s.newRepBuilder(st.Size(),func(id [32]byte)*repDesc{return descs[id]},cat)'
assert old in p
p=p.replace(old,'man,e:=s.readVerifiedManifest(entry.manifestUUID,entry.manifestSHA256);auditMust(e);ra:=s.newManifestReaderAt(man);defer ra.close();'+old)
p=p.replace('auditEncode(b,f,st.Size(),kind)','auditEncode(b,ra,st.Size(),kind)')
p=p.replace('"disk_after_rep":auditDisk(store)','"disk_after_rep":auditDisk(store),"store_floor":s.format.StoreFormatVersion,"analysis_inspected_bytes":b.budget.inspected,"production_sha256":"'+hashlib.sha256(s.encode()).hexdigest()+'"')
p+='\nfunc auditEncode(b *repBuilder,r io.ReaderAt,size int64,kind string)(*repObject,error){switch kind{case "dex":return b.encodeKind(repKindDEX,r,size);case "clr":return b.encodeKind(repKindCLR,r,size)};return nil,errRepUnsupported}\n'
(w/'probe.go').write_text(p)
subprocess.run([go,'fmt',str(w/'probe.go')],check=True,capture_output=True)
subprocess.run([go,'build','-o',str(w/'probe'),str(w/'production.go'),str(w/'probe.go')],check=True)
(w/'build-provenance.json').write_text(json.dumps({'production_sha256':hashlib.sha256(s.encode()).hexdigest(),'probe_sha256':hashlib.sha256((w/'probe.go').read_bytes()).hexdigest(),'source_changes':'production main renamed only; A0 probe uses actual CDC manifest ReaderAt through encodeKind; no producer or compiler inside measurements'},indent=2)+'\n')
