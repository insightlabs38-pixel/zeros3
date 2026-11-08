#!/bin/sh
# Production GET / range matrix (workstream C). Every response is byte-verified inside the window.
. "$(dirname "$0")/lib.sh"
F=get.jsonl
# whole-object GET, loose CAS. warm = page cache hot, cold = drop_caches + fresh process
for cache in warm cold; do
 getrun get_1m_whole_${cache}_c1  $F -objs 300 -size 1mib -mode whole -conc 1 -cache $cache -reps 5
 getrun get_1m_whole_${cache}_c8  $F -objs 300 -size 1mib -mode whole -conc 8 -cache $cache -reps 5
 getrun get_16m_whole_${cache}_c1 $F -objs 24  -size 16mib -mode whole -conc 1 -cache $cache -reps 5
 getrun get_16m_whole_${cache}_c4 $F -objs 24  -size 16mib -mode whole -conc 4 -cache $cache -reps 5
 getrun get_4k_whole_${cache}_c1  $F -objs 3000 -size 4kib -mode whole -conc 1 -cache $cache -reps 5
 getrun get_4k_whole_${cache}_c8  $F -objs 3000 -size 4kib -mode whole -conc 8 -cache $cache -reps 5
done
# sequential streaming of a large object stored by direct pack (>=64 MiB) and loose (forced)
for cache in warm cold; do
 getrun get_1g_seq_pack_${cache}   $F -objs 1 -size 1gib  -mode whole -conc 1 -cache $cache -reps 3 -nreq 3
 getrun get_256m_seq_loose_${cache} $F -objs 1 -size 256mib -mode whole -conc 1 -cache $cache -reps 3 -nreq 3 -env ZEROS3_DIRECT_PACK_MIB=0
done
# small random ranges: loose CAS vs packed CAS vs compressed packs
for r in 4kib 64kib 1mib; do
 getrun range_${r}_loose_1m    $F -objs 300 -size 1mib  -mode range -rsize $r -nreq 2000 -conc 1 -cache cold -reps 5
 getrun range_${r}_loose_64m   $F -objs 6 -size 64mib -mode range -rsize $r -nreq 2000 -conc 1 -cache cold -reps 5 -env ZEROS3_DIRECT_PACK_MIB=0
 getrun range_${r}_directpack  $F -objs 6 -size 64mib -mode range -rsize $r -nreq 2000 -conc 1 -cache cold -reps 5
 getrun range_${r}_compact_raw $F -objs 300 -size 1mib -mode range -rsize $r -nreq 2000 -conc 1 -cache cold -reps 5 -prep compact-off
 getrun range_${r}_compact_defl $F -objs 300 -size 1mib -kind text -mode range -rsize $r -nreq 2000 -conc 1 -cache cold -reps 5 -prep compact
 getrun range_${r}_loose_text  $F -objs 300 -size 1mib -kind text -mode range -rsize $r -nreq 2000 -conc 1 -cache cold -reps 5
done
# warm range + concurrency scaling
for c in 1 2 4 8 16 32; do
 getrun range_64k_warm_c$c $F -objs 300 -size 1mib -mode range -rsize 64kib -nreq 4000 -conc $c -cache warm -reps 5
done
# compressed pack whole reads (decompression cost) vs raw pack
getrun get_1m_text_pack_defl_warm $F -objs 300 -size 1mib -kind text -mode whole -conc 4 -cache warm -reps 5 -prep compact
getrun get_1m_text_pack_raw_warm  $F -objs 300 -size 1mib -kind text -mode whole -conc 4 -cache warm -reps 5 -prep compact-off
getrun get_1m_text_loose_warm     $F -objs 300 -size 1mib -kind text -mode whole -conc 4 -cache warm -reps 5
# hot repeated reads (zipf)
getrun hot_1m_zipf_warm $F -objs 300 -size 1mib -mode hot -nreq 3000 -conc 4 -cache warm -reps 5
# mixed GET/PUT
mixrun mixed_r4w2_64k mixed.jsonl -objs 300 -size 1mib -readers 4 -writers 2 -wsize 1mib -rsize 64kib -dur 20s -reps 3 -cache warm
mixrun mixed_r2w4_whole mixed.jsonl -objs 300 -size 1mib -readers 2 -writers 4 -wsize 4mib -rsize 0 -dur 20s -reps 3 -cache warm
echo GET_MATRIX_DONE
