"""Native wimlib lookup oracle and bounded physical chunk inventory; not a decoder.
Usage: q13_inventory.py CACHE. All compressed bytes stay opaque.
"""
import json,pathlib,re,struct,sys
root=pathlib.Path(sys.argv[1]);rows=[]
for row in json.loads((root/'inputs.json').read_text()):
 p=pathlib.Path(row['path']);b=p.read_bytes();chunk=struct.unpack_from('<I',b,20)[0];resources={};native=[]
 for block in pathlib.Path(str(p)+'.lookup.txt').read_text().split('\n\n'):
  if 'Hash ' not in block:continue
  uid=re.search(r'Hash\s+= 0x([0-9a-f]{40})',block).group(1);solid=re.search(r'Solid resource\s+= (\d+) => (\d+) bytes @ offset (\d+)',block)
  if solid:orig,size,off=map(int,solid.groups());r=dict(off=off,size=size,orig=orig,solid=True,compressed=True)
  else:
   orig=int(re.search(r'Uncompressed size\s+= (\d+)',block).group(1));size=int(re.search(r'Compressed size\s+= (\d+)',block).group(1));off=int(re.search(r'Offset in WIM\s+= (\d+)',block).group(1));r=dict(off=off,size=size,orig=orig,solid=False,compressed='FLAG_COMPRESSED' in block)
  assert 208<=off<=len(b) and 0<size<=len(b)-off
  old=resources.setdefault((off,size),r);assert old==r;native.append(dict(sha1=uid,physical=[off,size],solid=bool(solid)))
 resource_spans=[];chunks=[]
 for r in sorted(resources.values(),key=lambda r:r['off']):
  off,size,orig=r['off'],r['size'],r['orig'];resource_spans.append([off,size]);assert orig<=128<<20
  if r['solid']:
   declared,cs,method=struct.unpack_from('<QII',b,off);assert declared==orig and cs>0;count=(orig+cs-1)//cs;assert 0<count<=65536 and 16+4*count<=size
   lengths=struct.unpack_from('<'+'I'*count,b,off+16);at=off+16+4*count
   for length in lengths:assert 0<length<=off+size-at;chunks.append([at,length]);at+=length
   assert at==off+size
  elif r['compressed']:
   assert chunk>0;count=(orig+chunk-1)//chunk;assert 0<count<=65536;w=8 if orig>0xffffffff else 4;table=(count-1)*w;assert table<size
   offsets=[0]+[int.from_bytes(b[off+i*w:off+(i+1)*w],'little') for i in range(count-1)]+[size-table]
   for a,z in zip(offsets,offsets[1:]):assert 0<=a<z<=size-table;chunks.append([off+table+a,z-a])
  else:chunks.append([off,size])
 oracles=[];seen=set()
 for f in row['source_tree']:
  if f['sha256'] in seen:continue
  seen.add(f['sha256']);oracles.append(dict(path=str(root/'trees'/row['stage']/f['path']),sha256=f['sha256'],bytes=f['bytes']))
 rows.append(dict(row,resource_spans=resource_spans,chunk_spans=sorted(chunks),native_resources=native,oracles=oracles))
(root/'inventory.json').write_text(json.dumps(rows,indent=1));print('inventoried',len(rows),'images')
