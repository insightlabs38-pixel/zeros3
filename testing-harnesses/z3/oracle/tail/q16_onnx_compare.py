"""Independent ONNX native metadata/payload parity. Usage: CACHE FRAME_CLI."""
import hashlib,json,pathlib,subprocess,sys,onnx
cache=pathlib.Path(sys.argv[1]);frame=sys.argv[2];rows=json.loads((cache/'q16-corpus/inputs.json').read_text());proofs=[]
for r in rows:
 if not r['family'].startswith('onnx-'):continue
 p=pathlib.Path(r['path']);plan=json.loads(subprocess.check_output([frame,str(p)]));model=onnx.load(p);all_native={t.name:t for t in model.graph.initializer};native={n:t for n,t in all_native.items() if t.raw_data};assert len(all_native)==len(model.graph.initializer)
 assert set(native)=={t['Name'] for t in plan['Tensors']} and not plan['External'] and plan['Opaque']==len(all_native)-len(native)
 for t in plan['Tensors']:
  n=native[t['Name']];assert n.data_type==t['Type'] and list(n.dims)==t['Dims'] and len(n.raw_data)==t['Len'] and hashlib.sha256(n.raw_data).hexdigest()==t['SHA']
 proofs.append(dict(stage=r['stage'],raw_initializers=len(native),typed_opaque=len(all_native)-len(native),native_metadata_and_payload_sha256='PASS'))
(cache/'q16-corpus/onnx-oracle.json').write_text(json.dumps(proofs,indent=1));print(json.dumps(proofs))
