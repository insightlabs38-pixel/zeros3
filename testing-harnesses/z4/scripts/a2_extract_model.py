"""Mechanical Model-only extraction from immutable #80 onto latest main.
Run once on a clean A2 checkout. Preserves original parser/decoder source text.
"""
import hashlib,json,pathlib,subprocess
root=pathlib.Path(__file__).resolve().parents[3];p=root/'zeros3.go';s=p.read_text();draft=subprocess.check_output(['git','show','43dfe419:zeros3.go'],cwd=root).decode()
def block(a,b):
 start=draft.index(a);end=draft.index(b,start+len(a));assert end>start
 return draft[start:end]
parts=[block('// Port source: zeros3-z3/st/st.go','// Port source: zeros3-z3/repz3/kinds/clr.go'),block('// ---- Q9 DRAFT ONLY: bounded streaming model analysis','// ---- Q9 DRAFT ONLY: mechanically reused managed grouping')]
assert 'repKindModel repKind = 8' not in s
s=s.replace('\trepKindZstd   repKind = 7','\trepKindZstd   repKind = 7\n\trepKindModel repKind = 8',1)
s=s.replace('k == repKindGzip || k == repKindZstd\n','k == repKindGzip || k == repKindZstd || k == repKindModel\n',1)
s=s.replace('func repDecoderFor(k repKind) (repDecoder, bool) {\n\tswitch k {','func repDecoderFor(k repKind) (repDecoder, bool) {\n\tswitch k {\n\tcase repKindModel:\n\t\treturn repMDmodelDecoder{}, true',1)
a='\tswitch k {\n\tcase repKindGit:\n\t\treturn b.encodeGit';assert s.count(a)==1;s=s.replace(a,'\tswitch k {\n\tcase repKindModel:\n\t\treturn b.encodeModel(src, size)\n\tcase repKindGit:\n\t\treturn b.encodeGit')
a='\tswitch {\n\tcase bytes.HasPrefix(hd, []byte("PACK"))';assert s[s.index('func repCandidate('):].count(a)==1
modelcase=block('\tcase bytes.HasPrefix(hd, []byte("GGUF"))','\tcase bytes.HasPrefix(hd, []byte("PACK"))')
at=s.index('func repCandidate(');s=s[:at]+s[at:].replace(a,'\tswitch {\n'+modelcase+'\tcase bytes.HasPrefix(hd, []byte("PACK"))',1)
s+='\n// ---- Model kind 8: mechanically extracted from immutable draft #80 ----\n'+''.join(parts)
s=s.replace('// ---- Q9 DRAFT ONLY: bounded streaming model analysis','// ---- Bounded streaming model analysis')
p.write_text(s)
q=root/'testing-harnesses/z4/testdata/a2-model';q.mkdir(exist_ok=True)
(q/'extraction.json').write_text(json.dumps(dict(commit='43dfe4192ef0b25d035fac0850af263a7f866beb',source_sha256=hashlib.sha256(draft.encode()).hexdigest(),extracted_block_sha256=[hashlib.sha256(v.encode()).hexdigest() for v in parts],extracted_lines=[v.count('\n') for v in parts]),indent=1)+'\n')
