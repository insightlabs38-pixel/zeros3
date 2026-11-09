#!/usr/bin/env python3
"""audit_segments.py TRACE STORE_DIR [--sequential] [--control NAME]

Durability-ordering audit of a real `strace -A -y -f -ttt` of a zeros3 server writing small objects
through segments (see run_audit_segments.sh). It models what a power loss may undo: file data is
durable only after an fsync of that file, a directory entry (create / link / unlink / rename) only
after an fsync of its directory made after the operation. Rules, checked in trace order:

  S1  every pwrite to a segment is covered by an fsync of that segment before the next pwrite to it
      (one write + one fsync per commit group, never two groups in one flush);
  S2  --sequential (one client): at every journal write no segment byte is unsynced, i.e. the
      journal frame that names a record is written only after the fsync that covers the record.
      With concurrent clients the next group may legitimately be in flight, so only the weaker
      form is checked: a segment fsync completed between the first segment write and the first
      journal write;
  S3  a new segment: its header is fsynced and the segments/ directory fsynced before the first
      record is appended to it;
  S4  seal: no unsynced byte on the segment when it is linked into packs/ (the sealed tail is
      fsynced first); packs/ is fsynced after the link and before the segment name is unlinked;
  S5  FORMAT.json is replaced (rename, then fsync of the store root) before the first segment file
      is created.

Exit status 1 and the violations if a rule fails. --control NAME first removes one barrier from the
real trace (negative control: the audit must then fail): drop-dirsync | drop-seg-fsync |
drop-pack-dirsync | drop-format-sync | drop-seal-fsync.

This is ordering evidence about a real run. It cannot prove device behaviour and is not a
power-loss experiment.
"""
import re, sys, collections

argv = sys.argv[1:]
seq = '--sequential' in argv
control = argv[argv.index('--control') + 1] if '--control' in argv else None
pos = [a for i, a in enumerate(argv) if not a.startswith('--') and not (i > 0 and argv[i - 1] == '--control')]
trace, store = pos[0], pos[1].rstrip('/')

line_re = re.compile(r'^(\d+)\s+(\d+\.\d+)\s+(.*)$')
call_re = re.compile(r'^(\w+)\((.*)\)\s+=\s+(-?\d+|\?)(?:[\s<].*)?$', re.S)
unfinished, events = {}, []
for raw in open(trace, errors='replace'):
    m = line_re.match(raw.rstrip('\n'))
    if not m: continue
    pid, ts, rest = int(m.group(1)), float(m.group(2)), m.group(3)
    if rest.endswith('<unfinished ...>'):
        unfinished[pid] = (ts, rest[:-len('<unfinished ...>')].rstrip()); continue
    mr = re.match(r'^<\.\.\. (\w+) resumed>\s*(.*)$', rest)
    if mr:
        if pid not in unfinished: continue
        uts, head = unfinished.pop(pid); rest = head + mr.group(2); ts = uts
    mc = call_re.match(rest)
    if mc: events.append((ts, pid, mc.group(1), mc.group(2), mc.group(3)))
events.sort(key=lambda e: e[0])

fd_re = re.compile(r'(\d+)<([^>]*)>')
def fdpath(a):
    m = fd_re.search(a)
    return m.group(2) if m else None
def quoted(a): return re.findall(r'"((?:[^"\\]|\\.)*)"', a)
def is_seg(p): return bool(p) and p.startswith(store + '/segments/') and p.endswith('.seg')
def is_journal(p): return bool(p) and p.endswith('/journal/visibility.log')

if control:
    out, done, seen_seal = [], False, False
    for e in events:
        ts, pid, name, a, ret = e
        p = fdpath(a)
        if name == 'link' or name == 'linkat': seen_seal = True
        if name == 'fsync' and not done:
            if control == 'drop-dirsync' and p == store + '/segments': done = True; continue
            if control == 'drop-pack-dirsync' and p == store + '/packs': done = True; continue
            if control == 'drop-seg-fsync' and is_seg(p) and not seen_seal: done = True; continue
            if control == 'drop-seal-fsync' and is_seg(p) and seen_seal is False and False: pass
        out.append(e)
    if control == 'drop-format-sync':
        # drop the store-root fsyncs after the last FORMAT.json replacement preceding the first segment creation
        first_seg = next((i for i, e in enumerate(out) if e[2] in ('openat', 'open') and '/segments/' in e[3] and 'O_CREAT' in e[3]), None)
        fmt = None
        for i, e in enumerate(out[:first_seg]):
            if e[2] in ('rename', 'renameat', 'renameat2') and quoted(e[3]) and quoted(e[3])[-1].endswith('/FORMAT.json'): fmt = i
        if fmt is not None:
            # every root fsync in the window (the one after the segments/ mkdir covers the rename too)
            out = [e for i, e in enumerate(out) if not (fmt < i < first_seg and e[2] == 'fsync' and fdpath(e[3]) == store)]
    if control == 'drop-seal-fsync':
        # remove the last fsync of a segment before its first link (the sealed-tail fsync)
        idx_link = next((i for i, e in enumerate(out) if e[2] in ('link', 'linkat')), None)
        if idx_link is not None:
            for i in range(idx_link - 1, -1, -1):
                if out[i][2] == 'fsync' and is_seg(fdpath(out[i][3])):
                    del out[i]; break
    events = out

viol = []
unsynced = collections.defaultdict(int)     # segment path -> bytes written since its last fsync
seg = {}                                    # segment path -> {created, hdr, dir, records}
dir_fsync_ts = collections.defaultdict(float)
format_replaced = None; format_synced = False
first_seg_create = None
first_seg_write = first_journal_write = None
fsync_between = False
link_ts = {}
n_seal = n_unlink = 0

for ts, pid, name, a, ret in events:
    if ret == '?': continue
    p = fdpath(a)
    if name in ('openat', 'open', 'creat'):
        q = quoted(a); path = q[0] if q else ''
        if path.startswith(store + '/segments/') and path.endswith('.seg') and 'O_CREAT' in a and not ret.startswith('-'):
            if first_seg_create is None:
                first_seg_create = ts
                if not format_synced:
                    viol.append('S5: first segment file created at %.6f before FORMAT.json was replaced and the store root fsynced' % ts)
            seg[path] = dict(created=ts, hdr=False, dir=False, records=0)
    elif name in ('rename', 'renameat', 'renameat2'):
        q = quoted(a)
        if q and q[-1].endswith('/FORMAT.json'):
            format_replaced, format_synced = ts, False
    elif name in ('link', 'linkat'):
        q = quoted(a)
        s = next((x for x in q if x.endswith('.seg')), None)
        if s and any(x.startswith(store + '/packs/') for x in q):
            n_seal += 1
            link_ts[s] = ts
            if unsynced[s] > 0:
                viol.append('S4: %s linked into packs/ at %.6f with %d unsynced bytes (sealed tail not fsynced)' % (s, ts, unsynced[s]))
    elif name in ('unlink', 'unlinkat'):
        q = quoted(a)
        for x in q:
            if x.endswith('.seg') and x in link_ts:
                n_unlink += 1
                if dir_fsync_ts[store + '/packs'] < link_ts[x]:
                    viol.append('S4: segment %s unlinked at %.6f before packs/ was fsynced after the link' % (x, ts))
    elif name == 'pwrite64' and is_seg(p):
        mo = re.search(r',\s*(\d+)\)?\s*$', a); off = int(mo.group(1)) if mo else -1
        st = seg.get(p)
        if st is not None and off > 16 and p not in link_ts:
            st['records'] += 1
            if not (st['hdr'] and st['dir']):
                viol.append('S3: first record appended to %s at %.6f before its header fsync and the segments/ directory fsync' % (p, ts))
            if unsynced[p] > 0 and not st.get('tail_phase'):
                viol.append('S1: pwrite to %s at %.6f while the previous one is not fsynced' % (p, ts))
        unsynced[p] += int(ret)
        if first_seg_write is None: first_seg_write = ts
    elif name == 'write' and is_journal(p):
        if first_journal_write is None: first_journal_write = ts
        if seq and any(v > 0 for v in unsynced.values()):
            viol.append('S2: journal write at %.6f while segment bytes are unsynced: %s' % (ts, {k: v for k, v in unsynced.items() if v}))
    elif name in ('fsync', 'fdatasync') and p:
        if is_seg(p):
            unsynced[p] = 0
            if p in seg: seg[p]['hdr'] = True
            if first_seg_write is not None and first_journal_write is None: fsync_between = True
        else:
            dir_fsync_ts[p] = ts
            if p == store + '/segments':
                for s, st in seg.items():
                    if st['hdr'] and st['created'] < ts: st['dir'] = True
            if p == store and format_replaced is not None and ts > format_replaced:
                format_synced = True

if not seq and first_seg_write and first_journal_write and not fsync_between:
    viol.append('S2: no segment fsync completed between the first segment write and the first journal write')
if control is None and n_seal and n_unlink < n_seal:
    viol.append('S4: %d segments linked into packs/ but only %d unlinked' % (n_seal, n_unlink))

print('events=%d segments=%d sealed=%d control=%s sequential=%s' % (len(events), len(seg), n_seal, control, seq))
if viol:
    print('VIOLATIONS (%d):' % len(set(viol)))
    for v in sorted(set(viol))[:30]: print('  ' + v)
    sys.exit(1)
print('OK: rules S1-S5 hold on this trace')
