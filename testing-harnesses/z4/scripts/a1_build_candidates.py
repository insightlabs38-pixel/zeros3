"""Mechanically reuse immutable draft decoders; patch only publication capability.
Research candidates, never deployment artifacts. ROOT WORK GO.
"""
import hashlib,json,pathlib,subprocess,sys
root,w,go=map(pathlib.Path,sys.argv[1:]);w.mkdir(parents=True,exist_ok=True);records=[]
helper='''
// Research capability contract. A2/A3/A4 must earn their supported ceiling.
func (s *Store) z4PublicationFloor(r *repRoot) (int, error) {
 src:=s.newRepSource(r.Descs,nil);defer src.close()
 env:=&repEnv{Src:src,Limits:repProdLimits}
 reach,err:=env.reachable(r.Rep);if err!=nil{return 0,err}
 floor:=6
 for id,role:=range reach.Role {if role!=repDepRep{continue};obj,err:=env.loadRep(id);if err!=nil{return 0,err}
  switch obj.Kind {case 1,2,3,4,5,6,7:case 8:floor=max(floor,7);case 9,10:floor=max(floor,8);case 11,12:floor=max(floor,9);default:return 0,fmt.Errorf("%w: capability kind %d",errRepUnsupported,obj.Kind)}
 }
 if floor>z4SupportedFloor{return 0,fmt.Errorf("%w: required capability floor %d",errRepUnsupported,floor)}
 return floor,nil
}
'''
for name,commit,ceiling in [('model','43dfe4192ef0b25d035fac0850af263a7f866beb',7),('structured','0a37846c610fa4268548957da7b0fde3c0d9c249',8),('managed','43dfe4192ef0b25d035fac0850af263a7f866beb',9)]:
 d=w/name;d.mkdir(exist_ok=True);raw=subprocess.check_output(['git','show',commit+':zeros3.go'],cwd=root);s=raw.decode()
 s=s.replace('v <= storeFormatVersionRepresentations','v <= z4SupportedFloor').replace('storeFormatVersion, storeFormatVersionRepresentations)','storeFormatVersion, z4SupportedFloor)')
 marker='func (s *Store) publishRepRoot(r *repRoot) error {';assert s.count(marker)==1
 s=s.replace(marker,marker+'\n floor,err:=s.z4PublicationFloor(r);if err!=nil{return err}\n fireTestHook("z4-before-capability")')
 # err already declared above.
 s=s.replace('data, err := r.marshal()','data, err := r.marshal()')
 anchor='\ts.rep.mayHaveRoots.Store(true)\n\tfireTestHook(hookRepRootAfterFormat)';assert s.count(anchor)==1
 s=s.replace(anchor,'\tif err:=s.ensureStoreFormat(floor);err!=nil{return err}\n'+anchor)
 s+='\nconst z4SupportedFloor = '+str(ceiling)+'\n'+helper
 (d/'stock.go').write_text(s)
 subprocess.run([str(go),'build','-o',str(d/'zeros3'),str(d/'stock.go')],check=True)
 # Reuse the full phase probe with only main renamed in the private product copy.
 (d/'production.go').write_text(s.replace('func main() {','func legacyProductMain() {',1))
 probe=(w.parent/'z4-a0'/('i3' if name=='structured' else 'i2')/'probe.go').read_text();(d/'probe.go').write_text(probe)
 subprocess.run([str(go),'build','-o',str(d/'probe'),str(d/'production.go'),str(d/'probe.go')],check=True)
 records.append(dict(name=name,commit=commit,source_sha256=hashlib.sha256(raw).hexdigest(),candidate_sha256=hashlib.sha256(s.encode()).hexdigest(),ceiling=ceiling))
(w/'build-provenance.json').write_text(json.dumps(records,indent=1)+'\n')
