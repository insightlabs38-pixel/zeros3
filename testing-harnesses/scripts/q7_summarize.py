"""Validate the shared frontier and archive convergence measurements. MODULE_ROOT WORK OUT."""
import collections, gzip, hashlib, json, pathlib, sys
root, work, out = map(pathlib.Path, sys.argv[1:])
out.mkdir(parents=True, exist_ok=True)
old = json.load(gzip.open(root / 'testing-harnesses/testdata/q6-admission/frontier.json.gz'))
new = json.load(gzip.open(work / 'frontier.json.gz'))
for corpus in (old, new):
    for case in corpus['Cases']:
        for stage in case['Stages']:
            for candidate in stage['Candidates']:
                candidate.pop('AnalysisNS')
assert old == new, 'Q6 frontier changed beyond analysis timing'
rows = [json.loads(line) for line in open(work / 'final.jsonl')]
assert len(rows) == 37
summary = {}
for cls in dict.fromkeys(r['Class'] for r in rows):
    rs = [r for r in rows if r['Class'] == cls]
    last = rs[-1]
    sums = {k: sum(r[k] for r in rs) for k in ('Components', 'ExactComponents', 'Raw', 'ExactRaw', 'SpanRaw', 'SpanDescriptors', 'SpanCount', 'SpanBackingRead', 'SpanRequested', 'PackBackingRead', 'PackRequested', 'PackWorstDead', 'PackIndex')}
    summary[cls] = dict(stages=len(rs), final=last, totals=sums)
resource = json.load(open(work / 'resource.json'))
assert resource['exit'] == 0
proof = dict(resource=resource, q6_frontier_equal_excluding_timing=True,
             q6_frontier_sha256=hashlib.sha256((root / 'testing-harnesses/testdata/q6-admission/frontier.json.gz').read_bytes()).hexdigest(),
             provenance='same pinned Q6 public/native/Git inputs and evidence labels',
             scope='37 stages; selected candidate per class; all roots and dependencies proved by shared capture',
             costing='lab.PackedSize +64/object; descriptor reference byte counts; common authoritative root header excluded; no new persistent format',
             summary=summary)
for name, value in [('measurements.json.gz', rows), ('summary.json.gz', proof)]:
    with gzip.GzipFile(filename=str(out / name), mode='wb', mtime=0) as f:
        f.write(json.dumps(value, sort_keys=True, separators=(',', ':')).encode())
print(json.dumps(proof, indent=2))
