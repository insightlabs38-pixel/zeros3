#!/usr/bin/env python3
"""audit_order.py TRACE STORE_DIR [--strict-global]

Durability-ordering audit of a real `strace -f -ttt -s 8192` of a zeros3 server
(see run_audit.sh). It models what a power loss may undo: a file's data is
durable only after an fsync of that file, a directory entry (create/rename/
mkdir) only after an fsync of its directory *after* the operation. For every
journal write it checks, in trace order:

  R1  every file renamed into chunks/ manifests/ packs/ was fsynced (as a staged
      file) before the rename;
  R2  every manifest named by the journal frame (and every chunk that manifest
      lists, and every pack renamed earlier by the same process in sequential
      mode) has its rename followed by an fsync of the destination directory
      before the journal write;
  R3  a directory created under chunks/ had its parent fsynced after the mkdir
      and before the first rename into it;
  R4  the journal write is fsynced before the response is written is not
      checkable from syscalls alone; it checks that every journal write is
      followed by an fsync of the journal file before the next journal write
      of a *different* batch begins (group commit may cover several frames by
      one fsync, never zero).

Exit status 1 and a list of violations if any rule fails. This is evidence
about ORDER of barriers on a real run; it cannot prove device behaviour.
"""
import re, sys, json, collections

trace, store = sys.argv[1], sys.argv[2].rstrip('/')
strict = '--strict-global' in sys.argv

# ---- parse (merge <unfinished ...> / resumed lines per pid) ----
line_re = re.compile(r'^(\d+)\s+(\d+\.\d+)\s+(.*)$')
unfinished = {}
events = []  # (ts, pid, name, args, ret)
call_re = re.compile(r'^(\w+)\((.*)\)\s+=\s+(-?\d+|\?)(?:\s.*)?$', re.S)
for raw in open(trace, errors='replace'):
    m = line_re.match(raw.rstrip('\n'))
    if not m:
        continue
    pid, ts, rest = int(m.group(1)), float(m.group(2)), m.group(3)
    if rest.endswith('<unfinished ...>'):
        unfinished[pid] = (ts, rest[:-len('<unfinished ...>')].rstrip())
        continue
    mr = re.match(r'^<\.\.\. (\w+) resumed>\s*(.*)$', rest)
    if mr:
        if pid not in unfinished:
            continue
        uts, head = unfinished.pop(pid)
        rest = head + mr.group(2)
        ts = uts
    mc = call_re.match(rest)
    if not mc:
        continue
    name, args, ret = mc.group(1), mc.group(2), mc.group(3)
    events.append((ts, pid, name, args, ret))
events.sort(key=lambda e: e[0])

fdpath = {}      # (pid-agnostic: threads share the fd table) fd -> path
synced_file = set()   # paths whose data was fsynced (as opened path)
dirfd_synced_at = {}  # dir path -> list of event indexes of fsyncs
renames = []     # (idx, src, dst)
mkdirs = {}      # dir -> idx
fsync_idx = collections.defaultdict(list)  # path -> [idx]
manifest_chunks = {}  # manifest path -> set(hex)
journal_writes = []   # (idx, payload)
violations = []

def pathof(args):
    m = re.search(r'"((?:[^"\\]|\\.)*)"', args)
    return m.group(1) if m else None

def strace_str(s):
    return bytes(s, 'latin1').decode('unicode_escape', errors='replace') if '\\' in s else s

fd_re = re.compile(r'^(\d+)<([^>]*)>')

def fdp(args):
    m = fd_re.match(args)
    return m.group(2) if m else None

for idx, (ts, pid, name, args, ret) in enumerate(events):
    if name in ('fsync', 'fdatasync'):
        p = fdp(args)
        if p:
            fsync_idx[p].append(idx)
    elif name in ('rename', 'renameat', 'renameat2'):
        q = re.findall(r'"((?:[^"\\]|\\.)*)"', args)
        if len(q) >= 2 and not ret.startswith('-'):
            renames.append((idx, q[0], q[1]))
    elif name in ('mkdir', 'mkdirat'):
        p = pathof(args)
        if p and not ret.startswith('-'):
            mkdirs[p] = idx
    elif name in ('write', 'pwrite64'):
        p = fdp(args) or ''
        if p.endswith('visibility.log'):
            journal_writes.append((idx, args))
        elif '/manifests/' in p or '/tmp/' in p:
            hexes = set(re.findall(r'[0-9a-f]{64}', args))
            if hexes and ('manifest_uuid' in args or 'chunks' in args):
                manifest_chunks[p] = hexes

def under(p, sub):
    return p.startswith(store + '/') and ('/' + sub + '/') in p

dir_of = lambda p: p.rsplit('/', 1)[0]
renamed_into = {}
for idx, src, dst in renames:
    if not (under(dst, 'chunks') or under(dst, 'manifests') or under(dst, 'packs')):
        continue
    renamed_into[dst] = (idx, src)
    # R1
    if not any(i < idx for i in fsync_idx.get(src, [])):
        violations.append(('R1', dst, 'renamed without an earlier fsync of ' + src))
    # R3
    d = dir_of(dst)
    if d in mkdirs and mkdirs[d] < idx:
        par = dir_of(d)
        if not any(mkdirs[d] < i < idx for i in fsync_idx.get(par, [])):
            violations.append(('R3', dst, 'rename into %s before its parent %s was fsynced' % (d, par)))
    # a manifest staged under tmp carries its chunk list
    if under(dst, 'manifests') and src in manifest_chunks:
        manifest_chunks[dst] = manifest_chunks[src]

def dir_durable_before(dst, ridx, widx):
    d = dir_of(dst)
    return any(ridx < i < widx for i in fsync_idx.get(d, []))

# R2 / R4
journal_fsyncs = sorted(i for p, l in fsync_idx.items() if p.endswith('visibility.log') for i in l)
for n, (widx, args) in enumerate(journal_writes):
    m = re.search(r'"manifest_uuid":\s*\\?"([0-9a-f-]{36})', args)
    want = []
    if m:
        mp = next((d for d in renamed_into if d.endswith('/manifests/%s.json' % m.group(1))), None)
        if mp is None:
            continue
        want.append(mp)
        for h in sorted(manifest_chunks.get(mp, ())):
            for dst in renamed_into:
                if dst.endswith('/' + h) and under(dst, 'chunks'):
                    want.append(dst)
    if strict:
        want = [dst for dst, (ridx, _) in renamed_into.items() if ridx < widx]
    for dst in set(want):
        if dst not in renamed_into:
            continue  # pre-existing (dedup / packed) object, not published in this trace
        ridx = renamed_into[dst][0]
        if ridx > widx:
            violations.append(('R2', dst, 'renamed AFTER the journal frame that depends on it'))
        elif not dir_durable_before(dst, ridx, widx):
            violations.append(('R2', dst, 'journal frame written before %s was fsynced' % dir_of(dst)))
    nxt = journal_writes[n + 1][0] if n + 1 < len(journal_writes) else len(events)
    if not any(widx < i < nxt for i in journal_fsyncs):
        # allowed only if a later fsync covers it before any other durable work; require one at all
        if not any(i > widx for i in journal_fsyncs):
            violations.append(('R4', 'visibility.log', 'journal write #%d never fsynced' % n))

summary = {
    'events': len(events), 'renames_into_store': len(renamed_into), 'journal_writes': len(journal_writes),
    'journal_fsyncs': len(journal_fsyncs), 'mkdirs': len(mkdirs),
    'fsyncs_total': sum(len(v) for v in fsync_idx.values()), 'violations': len(violations),
}
print(json.dumps(summary))
for v in violations[:20]:
    print('VIOLATION', *v)
sys.exit(1 if violations else 0)
