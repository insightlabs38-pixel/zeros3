#!/bin/sh
P3_ROOT=${P3_ROOT:-/tmp/claude-0/p3}   # fixtures in $P3_ROOT/fix, binaries in $P3_ROOT/{base,new}, raw results in $P3_ROOT/res
# fixture stores through the real server (base binary). Each is built once; ops run on cp -a clones.
B=$P3_ROOT/base; F=$P3_ROOT/fix; W=$P3_ROOT/work
mk() { n=$1; shift; [ -d $F/$n ] && return; $B/p0bench mkstore -label mk_$n -out $P3_ROOT/res/mkstore.jsonl -work $W -bin $B/zeros3 -profbin $B/zeros3-prof -dir $F/$n "$@" >/dev/null 2>$W/mk_$n.err || echo "FAIL $n" ; echo "done $n $(date +%T)"; }
mk s1k   -objs 1000  -sizes 4k:60,16k:25,64k:15 -revs 2 -editfrac 0.5 -delfrac 0.3 -conc 8
mk s10k  -objs 10000 -sizes 4k:60,16k:25,64k:15 -revs 2 -editfrac 0.5 -delfrac 0.3 -conc 8
mk hist  -objs 20 -size 256kib -revs 256 -editfrac 0.02 -conc 4
mk loose10k -objs 10000 -sizes 4k:60,16k:25,64k:15 -revs 2 -editfrac 0.5 -delfrac 0.3 -conc 8 -env ZEROS3_SMALL_OBJECT_STORAGE=off,ZEROS3_DIRECT_PACK_KIB=0
mk mix   -objs 600 -sizes 4k:50,64k:30,1mib:15,16mib:5 -dupfrac 0.2 -revs 2 -editfrac 0.1 -delfrac 0.2 -conc 4 -kind text
mk s100k -objs 100000 -size 4kib -conc 16 -delfrac 0.3
mk s100kc -objs 100000 -size 4kib -conc 16                 # clean: the churn matrix starts from this one
mk s1m   -objs 1000000 -size 4kib -conc 16 -delfrac 0.3     # ~5 GB, ~3 minutes
