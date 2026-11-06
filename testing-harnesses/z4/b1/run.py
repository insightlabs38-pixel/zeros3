#!/usr/bin/env python3
"""Sequential fresh-process measurements; never benchmark concurrently."""
import argparse,json,pathlib,subprocess,sys
p=argparse.ArgumentParser();p.add_argument('--work',default='/workspace/work/z4-b1');p.add_argument('--binary',required=True);p.add_argument('--mode',default='cdc');p.add_argument('--manifest');a=p.parse_args();w=pathlib.Path(a.work);families=json.load(open(a.manifest or w/'manifest.json'));measure=pathlib.Path(__file__).with_name('measure.py')
for f in families:
 stem=w/(f['Name']+'-'+a.mode);mf=stem.with_suffix('.manifest.json');mf.write_text(json.dumps([f]));cmd=[sys.executable,str(measure),a.binary,'-manifest',str(mf),'-out',str(w/'stores'),'-mode',a.mode]
 with open(str(stem)+'.jsonl','w') as out,open(str(stem)+'.time','w') as err:subprocess.run(cmd,stdout=out,stderr=err,check=True)
 print(f['Name'],a.mode,'done',flush=True)
 with open(str(stem)+'.replay.jsonl','w') as out,open(str(stem)+'.replay.time','w') as err:subprocess.run([sys.executable,str(measure),a.binary,'-replay',str(w/'stores'/(f['Name']+'-'+a.mode))],stdout=out,stderr=err,check=True)
