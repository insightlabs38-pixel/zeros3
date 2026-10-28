"""Bounded native-codec oracle export; no candidate decoder calls this script."""
import hashlib,json,lzma,pathlib,sys
root=pathlib.Path(sys.argv[1]);rows=[]
def unpack(data,out):
    d=lzma.LZMADecompressor(memlimit=128<<20)
    raw=d.decompress(data,max_length=(128<<20)+1)
    if len(raw)>128<<20 or not d.eof or d.unused_data:raise ValueError('oracle limit/extra stream')
    out.write_bytes(raw)
    return dict(path=str(out),bytes=len(raw),sha256=hashlib.sha256(raw).hexdigest())
for item in json.loads((root/'discovery.json').read_text()):
    if 'error' in item:raise ValueError(item)
    p=root/item['file'];data=p.read_bytes()
    if hashlib.sha256(data).hexdigest()!=item['sha256']:raise ValueError('source changed')
    row=dict(item,family='coreutils' if p.suffix=='.xz' else 'hello-deb',path=str(p),oracles=[])
    if p.suffix=='.xz':row['oracles'].append(unpack(data,p.with_suffix('')))
    else:
        if data[:8]!=b'!<arch>\n':raise ValueError('ar magic')
        off=8
        while off<len(data):
            h=data[off:off+60]
            if len(h)!=60 or h[58:]!=b'`\n':raise ValueError('ar header')
            n=int(h[48:58]);name=h[:16].decode().strip().rstrip('/')
            if n<0 or off+60+n>len(data):raise ValueError('ar span')
            body=data[off+60:off+60+n]
            if name.endswith('.xz'):row['oracles'].append(unpack(body,root/(p.stem+'-'+name[:-3])))
            off+=60+n+(n&1)
        if off!=len(data):raise ValueError('ar tail')
    rows.append(row)
(root/'characterization-inputs.json').write_text(json.dumps(rows,indent=1))
print([(r['file'],sum(x['bytes'] for x in r['oracles'])) for r in rows])
