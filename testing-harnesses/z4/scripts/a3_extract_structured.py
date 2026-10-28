"""Mechanical extraction of immutable #81 onto cumulative Model-capable main."""
import hashlib,json,pathlib,subprocess
root=pathlib.Path(__file__).resolve().parents[3];p=root/'zeros3.go';s=p.read_text();raw=subprocess.check_output(['git','show','0a37846c:zeros3.go'],cwd=root);draft=raw.decode();marker='// ---- Q10 DRAFT ONLY: mechanically extracted structured codec cores';app=draft[draft.index(marker):];assert 'repKindSST' not in s
s=s.replace('\trepKindModel  repKind = 8','\trepKindModel  repKind = 8\n repKindSST repKind=9\n repKindParquet repKind=10',1)
s=s.replace('k == repKindZstd || k == repKindModel\n','k == repKindZstd || k == repKindModel || k==repKindSST || k==repKindParquet\n',1)
s=s.replace('func repDecoderFor(k repKind) (repDecoder, bool) {\n\tswitch k {','func repDecoderFor(k repKind) (repDecoder, bool) {\n\tswitch k {\n case repKindSST:return repSCsstDecoder{},true\n case repKindParquet:return repSCparquetDecoder{},true',1)
a='\tswitch k {\n\tcase repKindModel:\n\t\treturn b.encodeModel';assert s.count(a)==1;s=s.replace(a,'\tswitch k {\n case repKindSST,repKindParquet:return b.encodeStructured(src,size,k)\n\tcase repKindModel:\n\t\treturn b.encodeModel',1)
at=s.index('func repCandidate(');end=s.index('\nfunc ',at+10);body=s[at:end];body=body.replace('\tswitch {','\tswitch {\n case bytes.HasPrefix(hd,[]byte("PAR1")):return repKindParquet,true',1)
start=draft.index('func repCandidate(');tail=draft.index('\n\tif size >= 8 {',start);stop=draft.index('\n\treturn 0, false',tail);sst=draft[tail:stop];body=body.replace('\n\treturn 0, false',sst+'\n\treturn 0, false',1);s=s[:at]+body+s[end:]
s=s.replace('storeFormatVersionSupported       = storeFormatVersionModel','storeFormatVersionStructured = 8\n storeFormatVersionSupported = storeFormatVersionStructured',1)
s=s.replace('\t\tcase repKindModel:\n\t\t\tfloor = max(floor, storeFormatVersionModel)','\t\tcase repKindModel:\n\t\t\tfloor = max(floor, storeFormatVersionModel)\n case repKindSST,repKindParquet:floor=max(floor,storeFormatVersionStructured)',1)
s+='\n'+app.replace(marker,'// ---- Mechanically extracted structured codec cores from immutable #81 ----',1);p.write_text(s)
out=root/'testing-harnesses/z4/testdata/a3-structured';out.mkdir(exist_ok=True);(out/'extraction.json').write_text(json.dumps(dict(commit='0a37846c610fa4268548957da7b0fde3c0d9c249',source_sha256=hashlib.sha256(raw).hexdigest(),appended_sha256=hashlib.sha256(app.encode()).hexdigest(),appended_lines=app.count('\n')),indent=1)+'\n')
