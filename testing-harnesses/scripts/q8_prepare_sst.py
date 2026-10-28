"""Reuse Q6 scaled native operation logs; generate codec-separated history plans."""
import copy, json, pathlib, shutil, sys
src, dst = map(pathlib.Path, sys.argv[1:3]); dst.mkdir(parents=True, exist_ok=True)
for p in src.glob('*.ops'):
    shutil.copyfile(p, dst / p.name)
plan = json.load(open(src / 'plan.json'))
base = next(f for f in plan['families'] if f['name'] == 'chain')
plan['families'] = []
for codec in ['snappy', 'none', 'zstd', 'lz4']:
    family = copy.deepcopy(base); family['name'] = 'chain-' + codec
    for step in family['steps']:
        if step['c'] == 'newdb' and codec != 'snappy':
            step['codec'] = codec
    plan['families'].append(family)
json.dump(plan, open(dst / 'plan.json', 'w'), indent=1)
# Pebble has no LZ4 codec; this is an explicit producer subset, not a skipped failure.
pebble = copy.deepcopy(plan)
pebble['families'] = [f for f in pebble['families'] if f['name'] != 'chain-lz4']
(dst / 'pebble').mkdir(exist_ok=True)
for p in src.glob('*.ops'):
    shutil.copyfile(p, dst / 'pebble' / p.name)
json.dump(pebble, open(dst / 'pebble/plan.json', 'w'), indent=1)
