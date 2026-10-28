"""Bounded, commit-pinned Apache reference subset; failures are fatal. CACHE."""
import concurrent.futures, hashlib, json, pathlib, sys, urllib.request
root = pathlib.Path(sys.argv[1]); root.mkdir(parents=True, exist_ok=True)
pin = 'eedfad3f086678d282a00d8ff3cf8932d5d29af5'
legacy = (pathlib.Path(__file__).parents[1] / 'z3/oracle/parquet/fetch_ref.sh').read_text()
names = legacy.split('for f in ', 1)[1].split('; do', 1)[0].replace('\\\n', ' ').split()
names += ['concatenated_gzip_members.parquet', 'large_string_map.brotli.parquet', 'lz4_raw_compressed_larger.parquet', 'delta_encoding_optional_column.parquet', 'delta_encoding_required_column.parquet', 'int96_from_spark.parquet']
prior = {r['name']: r for r in json.load(open(root / 'provenance.json'))} if (root / 'provenance.json').exists() else {}
def fetch(name):
    p = root / name
    if p.exists() and name in prior:
        data = p.read_bytes()
        assert hashlib.sha256(data).hexdigest() == prior[name]['sha256']
        return prior[name]
    url = f'https://raw.githubusercontent.com/apache/parquet-testing/{pin}/data/{name}'
    with urllib.request.urlopen(url, timeout=45) as response:
        data = response.read((8 << 20) + 1)
    assert 0 < len(data) <= 8 << 20
    p.write_bytes(data)
    return dict(name=name, sha256=hashlib.sha256(data).hexdigest(), bytes=len(data), url=url, evidence='REFERENCE_FIXTURE')
with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
    rows = list(pool.map(fetch, names))
json.dump(rows, open(root / 'provenance.json', 'w'), indent=1)
print(len(rows), sum(r['bytes'] for r in rows))
