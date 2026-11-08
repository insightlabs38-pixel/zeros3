#!/usr/bin/env python3
"""Run one child, record wall/CPU/peak-RSS/IO/diskstats as one JSONL line. Stdlib only.
usage: rmeasure.py --label L --out F.jsonl [--cwd D] [--cache cold|warm] [--prof DIR] [--stdout FILE] [--reps N] -- CMD ARGS...
--prof DIR sets ZEROS3_P0_PROF_DIR/ZEROS3_P0_CPU for binaries built with the P0 overlay (build_research.sh *-prof).
Peak RSS = ru_maxrss of the child (OS high-water mark); IO = last /proc/PID/io sample (rchar/wchar) + block-device delta."""
import argparse, json, os, subprocess, sys, threading, time, hashlib

def diskstats(dev='vda'):
    for l in open('/proc/diskstats'):
        f = l.split()
        if len(f) > 9 and f[2] == dev:
            return int(f[5]) * 512, int(f[9]) * 512
    return 0, 0

def main():
    a = argparse.ArgumentParser()
    a.add_argument('--label', required=True); a.add_argument('--out', required=True)
    a.add_argument('--cwd'); a.add_argument('--cache', default='warm'); a.add_argument('--prof')
    a.add_argument('--stdout'); a.add_argument('--reps', type=int, default=1)
    a.add_argument('--pre', help='shell command run before each rep, outside the timed region')
    a.add_argument('cmd', nargs=argparse.REMAINDER)
    o = a.parse_args()
    cmd = o.cmd[1:] if o.cmd and o.cmd[0] == '--' else o.cmd
    for rep in range(o.reps):
        if o.pre:
            subprocess.run(o.pre, shell=True, check=True)
        if o.cache == 'cold':
            os.sync(); open('/proc/sys/vm/drop_caches', 'w').write('3')
        env = dict(os.environ)
        if o.prof:
            d = f'{o.prof}-r{rep}'; os.makedirs(d, exist_ok=True)
            env['ZEROS3_P0_PROF_DIR'] = d; env['ZEROS3_P0_CPU'] = '1'
        so = open(o.stdout, 'wb') if o.stdout else subprocess.DEVNULL
        r0, w0 = diskstats(); t0 = time.monotonic()
        p = subprocess.Popen(cmd, cwd=o.cwd, env=env, stdout=so, stderr=subprocess.STDOUT)
        peak = [0]; last = {}
        stop = threading.Event()
        def sample():
            while not stop.is_set():
                try:
                    for l in open(f'/proc/{p.pid}/status'):
                        if l.startswith('VmRSS:'): peak[0] = max(peak[0], int(l.split()[1]))
                    d = {}
                    for l in open(f'/proc/{p.pid}/io'):
                        k, v = l.split(':'); d[k] = int(v)
                    last.update(d)
                except Exception: pass
                time.sleep(0.02)
        th = threading.Thread(target=sample, daemon=True); th.start()
        _, status, ru = os.wait4(p.pid, 0)
        wall = time.monotonic() - t0; stop.set(); th.join()
        r1, w1 = diskstats()
        code = os.waitstatus_to_exitcode(status)
        row = dict(label=o.label, scenario='research', rep=rep, cmd=' '.join(cmd)[:400], exit=code, exit_ok=code == 0, wall_s=wall,
                   ru_user_s=ru.ru_utime, ru_sys_s=ru.ru_stime, ru_maxrss_mib=ru.ru_maxrss / 1024, sampled_peak_rss_mib=peak[0] / 1024,
                   io_rchar=last.get('rchar'), io_wchar=last.get('wchar'), dev_read_bytes=r1 - r0, dev_write_bytes=w1 - w0, cache=o.cache, note=os.environ.get('P0_NOTE'),
                   ts=time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()))
        if o.stdout:
            so.close(); row['stdout_bytes'] = os.path.getsize(o.stdout)
            row['stdout_tail'] = open(o.stdout, 'rb').read()[-600:].decode('utf8', 'replace')
        if o.prof:
            try: row['go_metrics'] = json.load(open(f'{o.prof}-r{rep}/metrics.json'))
            except Exception: pass
        print(json.dumps(row)); open(o.out, 'a').write(json.dumps(row) + '\n')
        if code != 0:
            print('FAILED', cmd, file=sys.stderr)
main()
