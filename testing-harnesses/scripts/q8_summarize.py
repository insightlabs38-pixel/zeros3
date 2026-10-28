"""Archive every input's proof/fallback and byte-weighted coverage. ROOT WORK OUT."""
import collections, gzip, hashlib, json, pathlib, sys
root, work, out = map(pathlib.Path, sys.argv[1:]); out.mkdir(parents=True, exist_ok=True)
rows = [json.loads(x) for x in open(work / 'frontiers.jsonl')]
assert len(rows) == 162 and len({r['Path'] for r in rows}) == 162
assert all(r['Proof'] for r in rows)
for r in rows:
    path = pathlib.Path(r['Path'])
    assert path.stat().st_size == r['Bytes']
    assert hashlib.sha256(path.read_bytes()).hexdigest() == r['SHA']
    r['Path'] = r['Path'].replace('/workspace/work/', '')
    r['Family'] = r['Family'].replace('/workspace/work/', '')
by = collections.defaultdict(list)
for r in rows: by[r['Family']].append(r)
summary = {k: dict(files=len(rs), fallback=sum(bool(r['Fallback']) for r in rs), final=rs[-1]) for k, rs in by.items()}
before = [json.loads(x) for x in open(work / 'duckdb-before-frontiers.jsonl')]
after = [r for r in rows if '/duckdb-variants/' in r['Path']]
assert len(before) == len(after) == 8
assert [(r['SHA'],r['Bytes']) for r in before] == [(r['SHA'],r['Bytes']) for r in after]
assert before[-1]['CDC'] == after[-1]['CDC']
summary['duckdb_counterfactual'] = dict(baseline_revision='c3efe6c387fcec3c6e37be327a627a83c9aee64d', old_rep=before[-1]['Representation'], new_rep=after[-1]['Representation'], unchanged_cdc=after[-1]['CDC'])
summary['fallback_reasons'] = dict(collections.Counter(r['Fallback'] for r in rows if r['Fallback']))
provenance = dict(reference=json.load(open('/workspace/work/q8-parquet/ref/provenance.json')),
    q6_operations='exact copied scaled Q6 operation logs; no new independent natural SST history',
    native={}, source={})
for cache in ['q8-sst', 'q8-sst-new', 'q8-parquet/parquet']:
    for p in pathlib.Path('/workspace/work', cache).rglob('meta.json'):
        record = json.load(open(p))
        record['requested_codec'] = p.parent.name.removeprefix('chain-')
        record['reported_options_are_template_defaults'] = True
        provenance['native'][str(p).replace('/workspace/work/', '')] = record
for p in pathlib.Path('/workspace/work/q8-ops').glob('*.ops'):
    provenance['source'][p.name] = hashlib.sha256(p.read_bytes()).hexdigest()
for p in pathlib.Path('/workspace/work/q8-pebble-new').glob('go.*'):
    provenance['source']['pebble-v2.1.7/' + p.name] = p.read_text()
for name, value in [('frontiers', rows), ('summary', summary), ('provenance', provenance)]:
    with gzip.GzipFile(filename=str(out / (name + '.json.gz')), mode='wb', mtime=0) as f:
        f.write(json.dumps(value, sort_keys=True, separators=(',', ':')).encode())
for name in ['parquet-before.txt', 'parquet-delta.txt', 'parquet-before.json', 'parquet-delta.json', 'parquet-rt.txt', 'sst-final-audit.txt', 'pebble-new-audit.txt', 'zstd-rt.txt', 'duckdb-before-frontiers.jsonl', 'delta-oracle.txt']:
    with gzip.GzipFile(filename=str(out / (name + '.gz')), mode='wb', mtime=0) as f:
        f.write((work / name).read_bytes())
print(json.dumps(summary, indent=2))
