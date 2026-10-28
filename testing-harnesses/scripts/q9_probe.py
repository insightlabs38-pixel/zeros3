"""Compile exact draft source with a private probe main; run one bounded cohort.
ROOT WORK GO KIND INPUT...; compile/runtime tools are test drivers, never decoders.
"""
import hashlib, json, pathlib, resource, subprocess, sys, time
root, work = map(pathlib.Path, sys.argv[1:3]); compiler, kind, *inputs = sys.argv[3:]
work.mkdir(parents=True, exist_ok=True)
source = (root / 'zeros3.go').read_text()
assert source.count('func main() {') == 1
(work / 'production.go').write_text(source.replace('func main() {', 'func zeros3DraftMain() {', 1))
(work / 'probe.go').write_bytes((root / 'testing-harnesses/probes/i2_probe.go.txt').read_bytes())
subprocess.run([compiler, 'build', '-o', str(work / 'probe'), str(work / 'production.go'), str(work / 'probe.go')], check=True)
# A fresh child isolates RSS from the compiler and prior probes.
pid = subprocess.Popen([str(work / 'probe'), kind, *inputs], stdout=open(work / 'results.jsonl', 'w'), stderr=open(work / 'probe.log', 'w'))
started = time.monotonic()
_, status, usage = __import__('os').wait4(pid.pid, 0)
code = __import__('os').waitstatus_to_exitcode(status)
pid.returncode = code
meta = dict(exit=code, seconds=time.monotonic()-started, maxrss_kib=usage.ru_maxrss,
            production_sha256=hashlib.sha256(source.encode()).hexdigest(),
            scope='model streaming or quarantined managed offline core; analyzer+CAS+serialized full proof+reachability',
            inputs=[dict(path=p, sha256=hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest(), bytes=pathlib.Path(p).stat().st_size) for p in inputs])
json.dump(meta, open(work / 'resource.json', 'w'), indent=1)
if code: raise SystemExit(code)
print(json.dumps(meta))
