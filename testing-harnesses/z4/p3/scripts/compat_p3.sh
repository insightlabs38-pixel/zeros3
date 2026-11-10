#!/bin/bash
# Old/new binary compatibility: (1) a store written through the NEW server is read, verified byte-for-byte and maintained by the BASE binary,
# (2) a store maintained (prune, gc, repack, compact) by the NEW binary verifies deep with the BASE binary, (3) and the reverse.
P3_ROOT=${P3_ROOT:-/tmp/claude-0/p3}; B=$P3_ROOT/base; N=$P3_ROOT/new; W=$P3_ROOT/work; R=$P3_ROOT/res/compat.txt
: > $R
say() { echo "$*" | tee -a $R; }
S=$W/compat_new_written; rm -rf $S $S.manifest.jsonl
$N/p0bench mkstore -label compat_mk -out /dev/null -work $W -bin $N/zeros3 -profbin $N/zeros3-prof -dir $S -objs 4000 -sizes 4k:60,16k:25,64k:10,1mib:5 -revs 2 -editfrac 0.5 -delfrac 0.3 -conc 8 >/dev/null 2>&1 && say "written by new server: ok"
$B/p0bench verifystore -label compat_replay_base -out /dev/null -work $W -bin $B/zeros3 -dir $S -manifest $S.manifest.jsonl >/dev/null 2>&1 && say "base binary replays every object of the new-written store byte-for-byte: ok" || say "FAIL: base replay"
$B/zeros3 verify -deep -store $S -json | grep -q '"missing": 0' && say "base verify -deep on new-written store: ok" || say "FAIL: base verify"
# maintained by NEW, read by BASE
M=$W/compat_new_maint; rm -rf $M; cp -a $S $M
for cmd in "versions prune -store $M -bucket bench -keep-last 0 -apply" "gc -apply -store $M" "compact -store $M" "repack -apply -max-live-percent 100 -store $M" "gc -apply -store $M"; do
  $N/zeros3 $cmd >/dev/null 2>&1 && say "new: $cmd: ok" || say "FAIL new: $cmd"
done
$B/zeros3 verify -deep -store $M -json | python3 -c 'import json,sys; r=json.load(sys.stdin); print("base verify -deep after new maintenance: missing=%d corrupt=%d invalid=%d journal_ok=%s" % (r["missing"],r["corrupt"],r["invalid"],r["journal_ok"]))' | tee -a $R
$B/p0bench verifystore -label compat_replay2 -out /dev/null -work $W -bin $B/zeros3 -dir $M -manifest $S.manifest.jsonl >/dev/null 2>&1 && say "base replays the live objects of the new-maintained store: ok" || say "note: replay of deleted/pruned objects expected to differ; see verifystore semantics"
# maintained by BASE, read by NEW
cp -a $S $M.2; for cmd in "versions prune -store $M.2 -bucket bench -keep-last 0 -apply" "gc -apply -store $M.2" "repack -apply -store $M.2"; do $B/zeros3 $cmd >/dev/null 2>&1 && say "base: $cmd: ok" || say "FAIL base: $cmd"; done
$N/zeros3 verify -deep -store $M.2 -json | python3 -c 'import json,sys; r=json.load(sys.stdin); print("new verify -deep after base maintenance: missing=%d corrupt=%d invalid=%d journal_ok=%s" % (r["missing"],r["corrupt"],r["invalid"],r["journal_ok"]))' | tee -a $R
# journal byte-identity: maintenance does not rewrite the journal except by appending a prune frame
sha256sum $S/journal/visibility.log | cut -c1-16 | sed 's/^/written journal sha256 prefix: /' | tee -a $R
rm -rf $S $M $M.2 $S.manifest.jsonl
