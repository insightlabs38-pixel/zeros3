"""Native wimlib lookup oracle and bounded physical chunk inventory; not a decoder.
Usage: q13_inventory.py CACHE. All compressed bytes stay opaque.
"""
import json,pathlib,re,struct,sys,hashlib,xml.etree.ElementTree as ET
def header(b):
 assert len(b)>=208 and b[:8]==b'MSWIM\0\0\0'
 cb,version,flags,chunk=struct.unpack_from('<IIII',b,8);assert cb==208
 part,total,images=struct.unpack_from('<HHI',b,40);assert part==total==1
 h=dict(header_bytes=cb,version=hex(version),flags=hex(flags),chunk_size=chunk,guid=b[24:40].hex(),part=part,parts=total,images=images)
 for name,pos in [('lookup',48),('xml',72),('boot_metadata',96),('integrity',124)]:
  value,off,orig=struct.unpack_from('<QQQ',b,pos);size=value&((1<<56)-1)
  assert off<=len(b) and size<=len(b)-off
  h[name]=dict(offset=off,stored=size,original=orig,flags=value>>56)
 xml=h['xml'];assert not xml['flags']&4
 text=b[xml['offset']:xml['offset']+xml['stored']].decode('utf-16le').lstrip('\ufeff').rstrip('\0')
 tree=ET.fromstring(text);h['xml_images']=len(tree.findall('IMAGE'));assert h['xml_images']==images
 integ=h['integrity'];assert integ['stored']>=12
 cb,count,cs=struct.unpack_from('<III',b,integ['offset']);assert cb==integ['stored']==12+20*count and 0<cs<=128<<20 and count<=65536
 end=h['lookup']['offset']+h['lookup']['stored'];assert xml['offset']>=end;assert end>=208 and count==(end-208+cs-1)//cs
 for i in range(count):
  start=208+i*cs;digest=hashlib.sha1(b[start:min(start+cs,end)]).digest();assert digest==b[integ['offset']+12+20*i:integ['offset']+32+20*i]
 h['integrity_verified']=True;h['integrity_coverage']=[208,end];h['integrity_excludes_current_xml_and_header']=True
 return h
root=pathlib.Path(sys.argv[1]);rows=[]
for row in json.loads((root/'inputs.json').read_text()):
 p=pathlib.Path(row['path']);b=p.read_bytes();assert hashlib.sha256(b).hexdigest()==row['sha256'];h=header(b);chunk=struct.unpack_from('<I',b,20)[0];resources={};native=[]
 for block in pathlib.Path(str(p)+'.lookup.txt').read_text().split('\n\n'):
  if 'Hash ' not in block:continue
  uid=re.search(r'Hash\s+= 0x([0-9a-f]{40})',block).group(1);solid=re.search(r'Solid resource\s+= (\d+) => (\d+) bytes @ offset (\d+)',block)
  if solid:orig,size,off=map(int,solid.groups());r=dict(off=off,size=size,orig=orig,solid=True,compressed=True)
  else:
   orig=int(re.search(r'Uncompressed size\s+= (\d+)',block).group(1));size=int(re.search(r'Compressed size\s+= (\d+)',block).group(1));off=int(re.search(r'Offset in WIM\s+= (\d+)',block).group(1));r=dict(off=off,size=size,orig=orig,solid=False,compressed='FLAG_COMPRESSED' in block)
  assert 208<=off<=len(b) and 0<size<=len(b)-off
  old=resources.setdefault((off,size),r);assert old==r;native.append(dict(sha1=uid,physical=[off,size],solid=bool(solid),metadata='FLAG_METADATA' in block))
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
 if 'source_oracles' in row:oracles=row['source_oracles']
 rows.append(dict(row,header=h,resource_spans=resource_spans,chunk_spans=sorted(chunks),native_resources=native,oracles=oracles))
(root/'inventory.json').write_text(json.dumps(rows,indent=1));print('inventoried',len(rows),'images')
