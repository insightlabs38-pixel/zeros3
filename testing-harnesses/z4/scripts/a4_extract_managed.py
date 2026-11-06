"""Mechanical reachable-code port plus the cumulative managed capability gate.
Run on the A4 source checkout; an existing extracted block is replaced in place.
"""
import argparse,hashlib,json,pathlib,subprocess,tempfile
ap=argparse.ArgumentParser();ap.add_argument('--go',default='/workspace/work/toolchain/go/bin/go');args=ap.parse_args()
root=pathlib.Path(__file__).resolve().parents[3];p=root/'zeros3.go';s=p.read_text()
start='// ---- Z4 A4 mechanically extracted managed codecs ----';stop='// ---- Z4 A4 managed store adapters ----'
with tempfile.TemporaryDirectory(prefix='z4-managed-port-') as tmp:
 out=pathlib.Path(tmp)/'port'
 subprocess.run([args.go,'run','./cmd/prodcodecport',str(root/'testing-harnesses/z3'),str(out),'managed'],cwd=root/'testing-harnesses/z3',check=True)
 decl=out.with_suffix('.decls').read_text();imports=out.with_suffix('.imports').read_text()
if start in s:
 a=s.index(start);b=s.index(stop,a);s=s[:a]+start+'\n'+decl+'\n'+s[b:]
else:
 assert 'repKindDEX' not in s
 s=s.replace('repKindParquet repKind = 10','repKindParquet repKind = 10\n repKindDEX repKind=11\n repKindCLR repKind=12',1)
 s=s.replace('k == repKindSST || k == repKindParquet\n','k == repKindSST || k == repKindParquet || k==repKindDEX || k==repKindCLR\n',1)
 marker='func repDecoderFor(k repKind) (repDecoder, bool) {\n\tswitch k {';assert marker in s
 s=s.replace(marker,marker+'\n case repKindDEX:return repMCdexDecoder{},true\n case repKindCLR:return repMCclrDecoder{},true',1)
 marker='\tswitch k {\n\tcase repKindSST, repKindParquet:';assert s.count(marker)==1
 s=s.replace(marker,'\tswitch k {\n case repKindDEX,repKindCLR:return b.encodeManaged(src,size,k)\n\tcase repKindSST, repKindParquet:',1)
 a=s.index('func repCandidate(');b=s.index('\nfunc ',a+10);body=s[a:b]
 body=body.replace('\tswitch {','\tswitch {\n case bytes.HasPrefix(hd,[]byte("dex\\n")):return repKindDEX,true\n case bytes.HasPrefix(hd,[]byte("MZ")):return repKindCLR,true',1);s=s[:a]+body+s[b:]
 marker='storeFormatVersionSupported       = storeFormatVersionStructured';assert marker in s
 s=s.replace(marker,'storeFormatVersionManaged = 9\n storeFormatVersionSupported = storeFormatVersionManaged',1)
 marker='\t\tcase repKindSST, repKindParquet:\n\t\t\tfloor = max(floor, storeFormatVersionStructured)';assert marker in s
 s=s.replace(marker,marker+'\n case repKindDEX,repKindCLR:floor=max(floor,storeFormatVersionManaged)',1)
 # Keep immutable draft recipe framing and expand semantics; creation is new.
 draft=subprocess.check_output(['git','show','43dfe419:zeros3.go'],cwd=root).decode()
 a=draft.index('// ---- Q9 DRAFT ONLY: managed recipe adapters, stricter budgets');b=draft.index('// ---- Q9 DRAFT ONLY: bounded streaming model analysis',a)
 adapter=draft[a:b].replace('// ---- Q9 DRAFT ONLY: managed recipe adapters, stricter budgets ----------------','// ---- Exact managed recipe adapters and decoder budgets -----------------------').replace('n > 1<<20','n > 16<<20').replace('a 512 MiB','a 32 MiB').replace('managed draft bounds','managed representation bounds')
 adapter=adapter.replace('if len(nb) == 0 ||','if int64(len(nb))!=m.Size{return nil,repCorrupt("managed node size")}\n if len(nb) == 0 ||')
 s+='\n'+start+'\n'+decl+'\n'+stop+'\n'+adapter
# Stream validated component groups through the bounded decoder getter.
for name,codec in [('clr','CL'),('dex','DX')]:
 a=s.index(f'func (repMC{name}Decoder) Decode(')
 if '\tcomps := make([][]byte, len(ids))' not in s[a:s.index('\nfunc ',a+1)]:continue
 x=s.index('\tcomps := make([][]byte, len(ids))',a);token=f'\n\tout, err := rep{codec}Decode(c.Rep.Recipe, comps)';y=s.index(token,x)+len(token)
 block='var total int64;for _,id:=range ids{n,e:=c.env.Src.Size(id);if e!=nil{return e};if n<=0||n>16<<20||total>(128<<20)-n{return fmt.Errorf("%w: managed component budget",errRepLimit)};total+=n};out,err:=rep'+codec+'DecodeFrom(c.Rep.Recipe,len(ids),func(i int)([]byte,error){return c.get(ids[i])})'
 s=s[:x]+block+s[y:]
# Only one new stdlib import is needed on cumulative main.
if '"container/heap"' not in s:s=s.replace('"compress/flate"','"compress/flate"\n "container/heap"',1)
p.write_text(s)
out=root/'testing-harnesses/z4/testdata/a4-managed';out.mkdir(exist_ok=True)
paths=[q for package in ['idxref','dexz3','clrz3','pez3','econx','lab'] for q in (root/'testing-harnesses/z3'/package).glob('*.go') if not q.name.endswith('_test.go')]
(out/'extraction.json').write_text(json.dumps({'source_commit':subprocess.check_output(['git','rev-parse','HEAD'],cwd=root,text=True).strip(),'declarations_sha256':hashlib.sha256(decl.encode()).hexdigest(),'declarations_lines':decl.count('\n'),'imports':imports,'source_sha256':{str(q.relative_to(root)):hashlib.sha256(q.read_bytes()).hexdigest() for q in paths}},indent=1)+'\n')
