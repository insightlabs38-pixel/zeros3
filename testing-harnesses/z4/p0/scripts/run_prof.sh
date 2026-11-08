#!/bin/sh
# Profiling runs (separate from timing runs). Raw pprof stays in $P0_WORK/prof, compact text goes to results/prof/.
. "$(dirname "$0")/lib.sh"
mkdir -p "$P0_RES/prof"
PF=prof.jsonl
sum() { for d in "$P0_WORK"/prof/$1-r*; do [ -d "$d" ] && "$(dirname "$0")/profsum.sh" "$d" "$P0_RES/prof/$(basename "$d").txt"; done; }
P() { l=$1; shift; putrun $l $PF -prof full -reps 1 "$@"; sum $l; }
G() { l=$1; shift; getrun $l $PF -prof full -reps 1 "$@"; sum $l; }
P prof_put_4k_c8      -objs 3000 -size 4kib  -conc 8
P prof_put_64k_c8     -objs 1500 -size 64kib -conc 8
P prof_put_1m_c1      -objs 300  -size 1mib  -conc 1
P prof_put_1m_c4      -objs 300  -size 1mib  -conc 4
P prof_put_16m_c1     -objs 24   -size 16mib -conc 1
P prof_put_64m_pack   -objs 8    -size 64mib -conc 1
P prof_put_1g         -objs 1    -size 1gib  -conc 1
P prof_put_1m_text    -objs 300  -size 1mib  -conc 4 -kind text
P prof_put_64m_text_auto -objs 4 -size 64mib -conc 1 -kind text -env ZEROS3_DIRECT_PACK_COMPRESS=auto
P prof_put_mp64m      -objs 4    -size 64mib -conc 1 -mode multipart -partconc 4
P prof_put_signed_1m  -objs 300  -size 1mib  -conc 4 -payload signed
P prof_put_rev16m     -objs 24   -size 16mib -conc 4 -revs 5 -editfrac 0.02
G prof_get_1m_whole_c8 -objs 300 -size 1mib -mode whole -conc 8 -cache warm
G prof_get_4k_c8       -objs 3000 -size 4kib -mode whole -conc 8 -cache warm
G prof_get_64m_pack_seq -objs 4 -size 64mib -mode whole -conc 1 -cache warm
G prof_range_4k_cold   -objs 300 -size 1mib -mode range -rsize 4kib -nreq 4000 -conc 1 -cache cold
G prof_range_64k_loose -objs 300 -size 1mib -mode range -rsize 64kib -nreq 4000 -conc 8 -cache warm
G prof_range_4k_pack   -objs 6 -size 64mib -mode range -rsize 4kib -nreq 4000 -conc 1 -cache warm
G prof_range_text_defl -objs 300 -size 1mib -kind text -mode range -rsize 64kib -nreq 3000 -conc 4 -cache warm -prep compact
echo PROF_DONE
