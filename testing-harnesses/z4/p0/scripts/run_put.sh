#!/bin/sh
# Production PUT matrix (workstream B). Timing runs use the plain binary.
. "$(dirname "$0")/lib.sh"
F=put.jsonl
# size ladder x concurrency (random = incompressible)
putrun put_4k_c1     $F -objs 3000 -size 4kib   -conc 1  -reps 5
putrun put_4k_c8     $F -objs 3000 -size 4kib   -conc 8  -reps 5
putrun put_64k_c1    $F -objs 1500 -size 64kib  -conc 1  -reps 5
putrun put_64k_c8    $F -objs 1500 -size 64kib  -conc 8  -reps 5
putrun put_1m_c1     $F -objs 300  -size 1mib   -conc 1  -reps 5
putrun put_1m_c4     $F -objs 300  -size 1mib   -conc 4  -reps 5
putrun put_1m_c8     $F -objs 300  -size 1mib   -conc 8  -reps 5
putrun put_16m_c1    $F -objs 24   -size 16mib  -conc 1  -reps 5
putrun put_16m_c4    $F -objs 24   -size 16mib  -conc 4  -reps 5
# direct-pack (>=64 MiB known length) vs forced loose vs pack compression
putrun put_64m_directpack      $F -objs 4 -size 64mib -conc 1 -reps 5
putrun put_64m_loose           $F -objs 4 -size 64mib -conc 1 -reps 5 -env ZEROS3_DIRECT_PACK_MIB=0
putrun put_64m_directpack_c4   $F -objs 8 -size 64mib -conc 4 -reps 3
putrun put_64m_text_pack_off   $F -objs 4 -size 64mib -conc 1 -reps 3 -kind text
putrun put_64m_text_pack_auto  $F -objs 4 -size 64mib -conc 1 -reps 3 -kind text -env ZEROS3_DIRECT_PACK_COMPRESS=auto
putrun put_1g_c1               $F -objs 1 -size 1gib  -conc 1 -reps 3
# compressibility, duplicates, similar revisions
putrun put_1m_text    $F -objs 300 -size 1mib -conc 4 -reps 5 -kind text
putrun put_1m_runs    $F -objs 300 -size 1mib -conc 4 -reps 5 -kind runs
putrun put_1m_dup50   $F -objs 300 -size 1mib -conc 4 -reps 5 -dupfrac 0.5
putrun put_1m_dup90   $F -objs 300 -size 1mib -conc 4 -reps 5 -dupfrac 0.9
putrun put_rev_16m    $F -objs 24 -size 16mib -conc 4 -reps 3 -revs 5 -editfrac 0.02
putrun put_rev_16m_e10 $F -objs 24 -size 16mib -conc 4 -reps 3 -revs 5 -editfrac 0.10
# transports
putrun put_mp_64m_pc1  $F -objs 4 -size 64mib -conc 1 -reps 3 -mode multipart -part 8mib -partconc 1
putrun put_mp_64m_pc4  $F -objs 4 -size 64mib -conc 1 -reps 3 -mode multipart -part 8mib -partconc 4
putrun put_chunked_16m $F -objs 24 -size 16mib -conc 4 -reps 3 -mode chunked
putrun put_signed_1m   $F -objs 300 -size 1mib -conc 4 -reps 5 -payload signed
# concurrency scaling at 1 MiB
for c in 2 16 32; do putrun put_1m_c$c $F -objs 512 -size 1mib -conc $c -reps 3; done
# mixed object-size population
putrun put_mixed_c4 $F -objs 1000 -size 1mib -sizes 4k:50,64k:30,1m:15,16m:5 -conc 4 -reps 3
# storage backing: tmpfs vs disk
mkdir -p /dev/shm/p0work
putrun put_tmpfs_1m_c4 $F -objs 300 -size 1mib -conc 4 -reps 5 -work /dev/shm/p0work
putrun put_tmpfs_4k_c8 $F -objs 3000 -size 4kib -conc 8 -reps 5 -work /dev/shm/p0work
rm -rf /dev/shm/p0work
echo PUT_MATRIX_DONE
