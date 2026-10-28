"""Mechanically extract immutable production sources and existing probe encoder switches.
ROOT CACHE GO; generated .go scratch stays outside repository modules.
"""
import hashlib,json,pathlib,subprocess,sys
root,cache=map(pathlib.Path,sys.argv[1:3]);go=sys.argv[3];out=cache/'z4-a0';out.mkdir(exist_ok=True);meta=[]
for key,draft,probe in [('i2',cache/'q9-report-fix','i2'),('i3',cache/'q10-report-fix','i3')]:
 source=(draft/'zeros3.go').read_text();original=(draft/f'testing-harnesses/probes/{probe}_probe.go.txt').read_text();start=original.index('switch currentKind{');stop=original.index('};probeMust(e);id,e=b.finishRep(r)',start) if key=='i2' else original.index('};if e!=nil{failure=e;return};id,e=b.finishRep(r)',start);switch=original[start:stop+1].replace('currentKind','kind').replace('probeMust','auditMust')
 wrapper='\nfunc auditEncode(b *repBuilder,f *os.File,size int64,kind string)(*repObject,error){info,e:=f.Stat();if e!=nil{return nil,e};var r *repObject;'+switch+';return r,e}\n'
 work=out/key;work.mkdir(exist_ok=True);(work/'production.go').write_text(source.replace('func main() {','func zeros3DraftMain() {',1));(work/'probe.go').write_text((root/'testing-harnesses/z4/probes/a0_phases.go.txt').read_text()+wrapper)
 subprocess.run([go,'fmt',str(work/'probe.go')],check=True,capture_output=True);subprocess.run([go,'build','-o',str(work/'probe'),str(work/'production.go'),str(work/'probe.go')],check=True)
 meta.append(dict(binary=key,production_sha256=hashlib.sha256(source.encode()).hexdigest(),draft=str(draft),encoder_switch_source_sha256=hashlib.sha256(original.encode()).hexdigest(),probe_sha256=hashlib.sha256((work/'probe.go').read_bytes()).hexdigest(),source_changes='main renamed only; private probe uses mechanically extracted old encoder switch'))
subprocess.run([go,'build','-o',str(out/'i1'),str(root/'zeros3.go')],check=True)
(out/'build-provenance.json').write_text(json.dumps(meta,indent=1));print(json.dumps(meta))
