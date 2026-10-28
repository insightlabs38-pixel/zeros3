"""Original native ONNX fixtures; external files validate at production only.
Usage: OUTPUT. Keep graph-only external fixture tests independent of its sidecar.
"""
import hashlib,json,pathlib,sys
import numpy as np,onnx
from onnx import TensorProto,helper,numpy_helper
out=pathlib.Path(sys.argv[1]);out.mkdir(parents=True,exist_ok=True);fixtures=[]
def make(typed=False):
 w=np.arange(256,dtype=np.float32).reshape(16,16)/256;b=np.arange(16,dtype=np.float32)
 tensors=[helper.make_tensor('W',TensorProto.FLOAT,w.shape,w.ravel().tolist()),helper.make_tensor('bias',TensorProto.FLOAT,b.shape,b.tolist())] if typed else [numpy_helper.from_array(w,'W'),numpy_helper.from_array(b,'bias')]
 graph=helper.make_graph([helper.make_node('MatMul',['X','W'],['Z']),helper.make_node('Add',['Z','bias'],['Y'])],'fixture',[helper.make_tensor_value_info('X',TensorProto.FLOAT,[1,16])],[helper.make_tensor_value_info('Y',TensorProto.FLOAT,[1,16])],tensors)
 return helper.make_model(graph,producer_name='ZeroS3 fixture',opset_imports=[helper.make_opsetid('',13)],ir_version=10)
for name in ['dense','reordered','typed','external','unknown']:
 model=make(name=='typed')
 if name=='reordered':values=list(model.graph.initializer);del model.graph.initializer[:];model.graph.initializer.extend(reversed(values))
 path=out/(name+'.onnx')
 if name=='external':onnx.save_model(model,path,save_as_external_data=True,all_tensors_to_one_file=True,location='weights.bin',size_threshold=0)
 else:
  b=model.SerializeToString()
  if name=='unknown':b+=b'\xc2\x3e\x0copaque bytes' # Model unknown field 1000, 12 bytes.
  path.write_bytes(b)
 onnx.checker.check_model(str(path));actual=onnx.load_model(path,load_external_data=False);b=path.read_bytes();fixtures.append(dict(file=path.name,sha256=hashlib.sha256(b).hexdigest(),tensors=[dict(name=t.name,type=t.data_type,dims=list(t.dims),raw_bytes=len(t.raw_data),raw_sha256=hashlib.sha256(t.raw_data).hexdigest(),external=t.data_location==TensorProto.EXTERNAL) for t in actual.graph.initializer],native_checker=True))
(out/'fixtures.json').write_text(json.dumps(dict(producer='onnx 1.19.1 / protobuf 6.33.0 / numpy '+np.__version__,external_sidecar_sha256=hashlib.sha256((out/'weights.bin').read_bytes()).hexdigest(),note='External sidecar proves native fixture validity here; graph-byte tests intentionally do not copy or resolve it.',fixtures=fixtures),indent=1))
