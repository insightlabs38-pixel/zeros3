# P1C generated tables


## Main matrix: P1B (5b9bcdd) vs P1C, 5 interleaved rounds (final_matrix.jsonl)

| cell | base | p1c | x | p50 ms | p90 ms | p99 ms | srv CPU-ms/obj | RSS MiB | files/obj | phys/logical |
|---|---:|---:|---:|---|---|---|---|---|---|---|
| put_1b_c8 (ops/s) | 1241 [1190-1394] | 4322 [4242-5224] | 3.48x | 6.0->1.7 | 9.7->2.4 | 15.8->3.8 | 1.88->0.27 | 19->20 | 1.13->0.00 | 873.01->922.74 |
| put_1k_c8 (ops/s) | 936 [902-988] | 4362 [4278-4763] | 4.66x | 8.2->1.7 | 12.1->2.4 | 16.9->3.5 | 2.45->0.27 | 22->22 | 2.00->0.00 | 1.86->1.95 |
| put_4k_c1 (ops/s) | 358 [336-388] | 913 [784-1018] | 2.55x | 2.6->1.0 | 3.7->1.4 | 5.2->2.3 | 3.06->0.70 | 17->18 | 2.00->0.01 | 1.22->1.24 |
| put_4k_c4 (ops/s) | 791 [783-816] | 2607 [2439-3197] | 3.30x | 4.8->1.4 | 7.0->2.0 | 10.1->3.1 | 2.64->0.40 | 19->22 | 2.00->0.00 | 1.22->1.24 |
| put_4k_c8 (ops/s) | 929 [927-985] | 4071 [3747-4380] | 4.38x | 8.1->1.9 | 12.4->2.6 | 17.8->4.2 | 2.49->0.29 | 21->23 | 2.00->0.00 | 1.22->1.24 |
| put_4k_c16 (ops/s) | 1099 [1042-1112] | 6461 [6378-6729] | 5.88x | 13.5->2.3 | 23.0->3.4 | 34.3->5.3 | 2.17->0.21 | 25->25 | 2.00->0.00 | 1.22->1.24 |
| put_4k_c32 (ops/s) | 1154 [1079-1211] | 8883 [8361-9748] | 7.70x | 25.5->3.3 | 45.4->4.9 | 69.7->8.5 | 2.08->0.17 | 28->28 | 2.00->0.00 | 1.22->1.24 |
| put_16k_c1 (ops/s) | 373 [358-384] | 770 [754-799] | 2.07x | 2.5->1.2 | 3.5->1.6 | 4.7->2.5 | 2.89->0.84 | 17->19 | 2.00->0.01 | 1.05->1.06 |
| put_16k_c8 (ops/s) | 839 [816-915] | 3477 [3227-3602] | 4.14x | 9.0->2.1 | 13.8->3.0 | 18.5->5.7 | 2.75->0.38 | 21->25 | 2.00->0.00 | 1.05->1.06 |
| put_16k_c32 (ops/s) | 1023 [982-1158] | 6642 [6081-7452] | 6.50x | 29.1->4.5 | 50.7->6.4 | 73.6->14.8 | 2.42->0.23 | 30->31 | 2.00->0.00 | 1.05->1.06 |
| put_64k_c1 (ops/s) | 277 [268-307] | 602 [546-615] | 2.17x | 3.3->1.5 | 5.1->2.0 | 7.3->4.2 | 3.95->1.20 | 17->20 | 2.63->0.01 | 1.01->1.02 |
| put_64k_c8 (ops/s) | 651 [587-673] | 2096 [1852-2258] | 3.22x | 11.8->3.5 | 17.8->5.1 | 29.1->12.4 | 3.60->0.75 | 21->25 | 2.58->0.01 | 1.01->1.02 |
| put_64k_c32 (ops/s) | 776 [770-812] | 2843 [2759-3098] | 3.67x | 37.9->9.9 | 63.4->15.0 | 91.8->25.5 | 3.16->0.58 | 29->40 | 2.59->0.00 | 1.01->1.02 |
| put_128k_c8 (ops/s) | 480 [464-526] | 1385 [1363-1482] | 2.88x | 15.3->5.3 | 23.8->7.9 | 39.2->16.2 | 4.56->1.18 | 21->28 | 3.62->0.01 | 1.01->1.01 |
| put_255k_c8 (ops/s) | 352 [329-388] | 931 [873-935] | 2.64x | 21.7->8.2 | 32.6->11.9 | 43.7->20.3 | 6.47->1.97 | 23->36 | 5.61->0.01 | 1.00->1.01 |
| put_256k_c8 (ops/s) | 522 [507-578] | 950 [922-965] | 1.82x | 14.7->8.0 | 21.0->11.6 | 26.2->18.3 | 4.49->1.99 | 41->36 | 2.00->0.01 | 1.01->1.01 |
| put_512k_c8 (ops/s) | 390 [378-420] | 556 [524-569] | 1.43x | 19.5->13.4 | 27.5->20.4 | 35.0->29.7 | 6.24->3.65 | 45->48 | 2.00->0.02 | 1.00->1.00 |
| put_1m_c1 (MiB/s) | 97 [97-97] | 105 [104-107] | 1.08x | 10.0->9.1 | 11.6->10.5 | 15.5->16.7 | 10.65->9.60 | 22->24 | 2.02->1.03 | 1.00->1.00 |
| put_1m_c4 (MiB/s) | 219 [215-231] | 257 [250-262] | 1.17x | 17.4->14.6 | 23.2->19.8 | 29.1->31.5 | 10.30->8.60 | 40->44 | 2.01->1.02 | 1.00->1.00 |
| put_1m_c16 (MiB/s) | 273 [262-275] | 314 [305-320] | 1.15x | 56.7->50.0 | 86.4->73.8 | 116.4->98.4 | 9.50->8.00 | 70->86 | 2.01->1.01 | 1.00->1.00 |
| put_4m_c4 (MiB/s) | 353 [339-363] | 360 [357-380] | 1.02x | 44.8->41.8 | 54.0->52.4 | 59.0->65.4 | 26.75->25.75 | 38->45 | 2.04->1.09 | 1.00->1.00 |
| put_64m_dp_c1 (MiB/s) | 404 [397-439] | 410 [404-435] | 1.02x | 156.5->150.6 | 159.3->155.7 | 159.3->155.7 | 400.00->390.00 | 35->35 | 4.00->4.25 | 1.00->1.00 |
| put_64m_dp_c4 (MiB/s) | 531 [498-546] | 501 [473-513] | 0.94x | 470.6->503.4 | 500.0->527.9 | 500.0->527.9 | 362.50->380.00 | 60->64 | 3.25->3.38 | 1.00->1.00 |
| put_1g_c1 (MiB/s) | 470 [463-477] | 481 [477-486] | 1.02x | 2178.7->2127.2 | 2178.7->2127.2 | 2178.7->2127.2 | 6160.00->5995.00 | 42->44 | 20.00->21.00 | 1.00->1.00 |
| put_mp_64m_pc4 (MiB/s) | 203 [194-208] | 207 [205-213] | 1.02x | 306.9->301.5 | 318.9->311.4 | 318.9->311.4 | 665.00->652.50 | 38->42 | 19.00->19.25 | 1.02->1.02 |
| put_4k_dup50_c8 (ops/s) | 1131 [1031-1132] | 4393 [3960-4456] | 3.89x | 6.9->1.7 | 10.2->2.4 | 13.9->4.3 | 2.02->0.28 | 22->23 | 1.66->0.00 | 0.88->0.90 |
| put_64k_text_c8 (ops/s) | 759 [665-765] | 2015 [1894-2189] | 2.65x | 10.2->3.7 | 14.7->5.4 | 20.8->11.4 | 3.07->0.76 | 21->26 | 2.11->0.01 | 1.01->1.02 |
| put_64k_rev_c4 (ops/s) | 484 [385-737] | 1332 [966-1718] | 2.75x | 7.9->2.7 | 10.6->3.9 | 12.3->4.3 | 4.38->1.00 | 19->21 | 10.88->0.17 | 0.86->0.86 |
| put_mixsizes_c4 (MiB/s) | 238 [229-243] | 334 [306-335] | 1.40x | 9.5->3.4 | 30.6->24.1 | 141.7->137.4 | 10.78->7.87 | 37->42 | 2.20->0.24 | 1.00->1.00 |
| get_4k_whole (ops/s) | 9223 [8360-9848] | 10397 [9376-10420] | 1.13x | 0.6->0.5 | 1.7->1.5 | 3.4->3.2 | 0.15->0.12 | 18->19 | 2.00->0.00 | nan->nan |
| get_1m_range64k (MiB/s) | 399 [373-417] | 406 [348-440] | 1.02x | 1.0->0.9 | 1.9->1.9 | 3.9->4.1 | 5.50->5.50 | 29->28 | 2.03->1.07 | nan->nan |

| mixed cell | reads/s | writes/s | read p99 ms | write p99 ms |
|---|---|---|---|---|
| mixed_4k_r4w2 | 4910->7091 (1.44x) | 455->928 (2.04x) | 3.3->2.4 | 8.7->5.5 |

## Large objects, 5 rounds with alternating order (big_order.jsonl)

| cell | base | p1c | x | p50 ms | p90 ms | p99 ms | srv CPU-ms/obj | RSS MiB | files/obj | phys/logical |
|---|---:|---:|---:|---|---|---|---|---|---|---|
| put_64m_dp_c1 (MiB/s) | 429 [412-450] | 411 [405-416] | 0.96x | 146.1->153.0 | 149.6->153.8 | 149.6->153.8 | 385.00->395.00 | 34->35 | 4.00->4.25 | 1.00->1.00 |
| put_64m_dp_c4 (MiB/s) | 516 [475-540] | 531 [510-544] | 1.03x | 480.9->478.7 | 516.6->489.2 | 516.6->489.2 | 373.75->367.50 | 64->62 | 3.25->3.38 | 1.00->1.00 |
| put_1g_c1 (MiB/s) | 475 [459-483] | 457 [451-477] | 0.96x | 2154.9->2241.9 | 2154.9->2241.9 | 2154.9->2241.9 | 6110.00->6300.00 | 43->44 | 20.00->21.00 | 1.00->1.00 |
| put_mp_64m_pc4 (MiB/s) | 208 [205-213] | 213 [202-217] | 1.03x | 299.0->297.8 | 306.6->305.3 | 306.6->305.3 | 650.00->647.50 | 39->42 | 19.00->19.25 | 1.02->1.02 |
| put_16m_c4 (MiB/s) | 407 [381-421] | 411 [393-420] | 1.01x | 155.8->153.1 | 174.9->174.8 | 177.1->178.2 | 92.50->93.33 | 40->45 | 2.12->1.29 | 1.00->1.00 |

## Ablation (ablation.jsonl): ops/s, median of 3 interleaved rounds

| variant | put_1m_c4 | put_4k_c8 | put_4k_c32 | put_4k_c1 | put_64k_c8 | put_255k_c8 |
|---|---:|---:|---:|---:|---:|---:|
| P1B | 235 | 954 | 1180 | 379 | 638 | 333 |
| P1C code, segments off (loose, pool on) | 244 | 1023 | 1396 | 399 | 731 | 365 |
| P1C, chunker pool off | 270 | 2320 | 3584 | 829 | 1442 | 876 |
| P1C (all) | 266 | 4686 | 10717 | 973 | 2282 | 906 |

## GOMAXPROCS 1 / 2 / 4 (gomaxprocs.jsonl): base -> P1C

| cell | 1 | 2 | 4 |
|---|---|---|---|
| put_1m_c4 | 109 -> 114 (1.05x) | 184 -> 217 (1.18x) | 221 -> 283 (1.28x) |
| put_4k_c8 | 625 -> 3371 (5.39x) | 884 -> 4401 (4.98x) | 987 -> 4627 (4.69x) |
| put_64k_c8 | 442 -> 1007 (2.28x) | 573 -> 2004 (3.49x) | 664 -> 2299 (3.46x) |

## Scale (scale.jsonl)

| variant | objects | delfrac | populate s | files | dirs | du MiB | open warm s | open cold s | open RSS MiB | verify -deep s | gc s | gc -apply s | repack -apply s | files after |
|---|---:|---:|---:|---:|---:|---:|---|---|---|---|---|---|---|---|
| base | 1000 | 0 | 1 | 2003 | 1248 | 13 | 0.02 | 0.02 | 12 | 0.17 | 0.03 | 0.03 | 0.02 | 2003 |
| final | 1000 | 0 | 0 | 7 | 14 | 5 | 0.02 | 0.03 | 15 | 0.06 | 0.03 | 0.02 | 0.03 | 7 |
| base | 10000 | 0 | 6 | 20003 | 9569 | 120 | 0.06 | 0.05 | 21 | 1.32 | 0.25 | 0.25 | 0.20 | 20003 |
| final | 10000 | 0 | 2 | 8 | 15 | 49 | 0.06 | 0.06 | 25 | 0.71 | 0.18 | 0.18 | 0.18 | 8 |
| base | 100000 | 0 | 53 | 200003 | 51548 | 1023 | 0.35 | 0.33 | 103 | 14.95 | 2.86 | 2.79 | 1.95 | 200003 |
| final | 100000 | 0 | 17 | 21 | 15 | 493 | 0.43 | 0.49 | 142 | 8.01 | 1.99 | 1.99 | 2.00 | 21 |
| base | 100000 | 0.5 | 54 | 200003 | 51548 | 1043 | 0.51 | 0.54 | 157 | 15.91 | 3.23 | 3.30 | 2.33 | 200003 |
| base | 100000 | 0.9 | 70 | 200003 | 51548 | 1060 | 0.73 | 0.64 | 192 | 16.11 | 3.40 | 3.26 | 2.44 | 200003 |
| final | 100000 | 0.5 | 17 | 21 | 15 | 513 | 0.64 | 0.70 | 192 | 8.33 | 2.12 | 2.17 | 2.30 | 21 |
| final | 100000 | 0.9 | 16 | 21 | 15 | 530 | 0.76 | 0.80 | 229 | 8.17 | 2.35 | 2.35 | 2.42 | 21 |
| final | 1000000 | 0 | 173 | 147 | 15 | 4931 | 4.45 | 4.40 | 1309 | 95.01 | 25.05 | 25.58 | 25.66 | 147 |
| base | 1000000 | 0 | 569 | 2000003 | 65798 | 8473 | 3.33 | 3.58 | 954 | 203.54 | 43.01 | 43.25 | 29.94 | 2000003 |

## Fixed batching window vs natural batching (prototype, 4 KiB, proto_delay.jsonl)

| concurrency | window | ops/s | p50 ms | p99 ms | avg group |
|---|---|---:|---:|---:|---:|
| 1 | 0 | 2094 | 0.43 | 1.21 | 1.0 |
| 1 | 100us | 546 | 1.80 | 3.20 | 1.0 |
| 1 | 2ms | 319 | 3.03 | 4.80 | 1.0 |
| 1 | 500us | 492 | 1.89 | 3.44 | 1.0 |
| 8 | 0 | 7715 | 0.98 | 1.83 | 2.2 |
| 8 | 100us | 6129 | 1.22 | 2.68 | 2.3 |
| 8 | 2ms | 2384 | 3.30 | 4.33 | 4.0 |
| 8 | 500us | 3482 | 2.19 | 5.32 | 3.8 |
| 32 | 0 | 17063 | 1.72 | 3.23 | 7.8 |
| 32 | 100us | 14611 | 2.04 | 4.41 | 8.4 |
| 32 | 2ms | 7915 | 3.95 | 7.02 | 15.9 |
| 32 | 500us | 11332 | 2.78 | 5.41 | 10.4 |

## Space reclamation under churn (churn.jsonl): 100,000 x 4 KiB objects, a fraction deleted, history pruned

| variant | deleted | stage | files | store MiB | wall s |
|---|---|---|---:|---:|---:|
| base | 0.5 | populated | 200003 | 1043 | - |
| base | 0.5 | pruned | 200003 | 1048 | 0.65 |
| base | 0.5 | after_gc | 99881 | 656 | 13.99 |
| base | 0.5 | after_repack | 99881 | 656 | 1.75 |
| final | 0.5 | populated | 21 | 513 | - |
| final | 0.5 | pruned | 21 | 518 | 0.94 |
| final | 0.5 | after_gc | 21 | 518 | 1.57 |
| final | 0.5 | after_repack | 17 | 413 | 3.28 |
| base | 0.9 | populated | 200003 | 1060 | - |
| base | 0.9 | pruned | 200003 | 1067 | 1.07 |
| base | 0.9 | after_gc | 19837 | 364 | 21.94 |
| base | 0.9 | after_repack | 19837 | 364 | 1.13 |
| final | 0.9 | populated | 21 | 530 | - |
| final | 0.9 | pruned | 21 | 538 | 1.06 |
| final | 0.9 | after_gc | 21 | 538 | 1.32 |
| final | 0.9 | after_repack | 7 | 121 | 2.34 |

## Syscalls per object (strace -c -f, whole server lifetime) (syscalls_per_object.txt)

```
### base size=4kib objs=1000 conc=8
per-object syscall counts (1000 objects; whole server lifetime incl. startup and bucket creation)
  fsync          5.66
  openat         5.95
  mkdirat        1.25
  renameat       2.00
  pwrite64       0.73
  write          3.02
  newfstatat     4.01
  close          5.95
### final size=4kib objs=1000 conc=8
per-object syscall counts (1000 objects; whole server lifetime incl. startup and bucket creation)
  fsync          0.88
  openat         0.03
  mkdirat        0.01
  renameat       0.00
  pwrite64       0.87
  write          1.12
  newfstatat     0.03
  close          0.03
### base size=64kib objs=600 conc=8
per-object syscall counts (600 objects; whole server lifetime incl. startup and bucket creation)
  fsync          7.67
  openat         8.57
  mkdirat        2.04
  renameat       2.63
  pwrite64       0.75
  write          3.64
  newfstatat     5.90
  close          8.56
### final size=64kib objs=600 conc=8
per-object syscall counts (600 objects; whole server lifetime incl. startup and bucket creation)
  fsync          0.97
  openat         0.07
  mkdirat        0.02
  renameat       0.01
  pwrite64       0.94
  write          1.10
  unlinkat       0.00
  newfstatat     0.06
  close          0.06
### base size=1mib objs=100 conc=4
per-object syscall counts (100 objects; whole server lifetime incl. startup and bucket creation)
  fsync          4.99
  openat         7.13
  mkdirat        0.06
  renameat       2.02
  pwrite64       1.01
  write          8.29
  newfstatat    19.46
  close          7.10
### final size=1mib objs=100 conc=4
per-object syscall counts (100 objects; whole server lifetime incl. startup and bucket creation)
  fsync          4.11
  openat         5.32
  mkdirat        0.14
  renameat       1.05
  pwrite64       1.99
  write          7.30
  newfstatat    18.66
  close          5.24
```

## Store layer, no HTTP (in-process, loose vs segments) (store_layer_bench.txt)

```
    zeros3_p1c_bench_test.go:84: P1C_STORE size=0 conc=8 loose=4284 ops/s segments=7798 ops/s x1.82  p99 3.41 -> 2.59 ms (medians of 3 interleaved rounds)
    zeros3_p1c_bench_test.go:84: P1C_STORE size=1 conc=8 loose=3813 ops/s segments=7743 ops/s x2.03  p99 7.93 -> 2.93 ms (medians of 3 interleaved rounds)
    zeros3_p1c_bench_test.go:84: P1C_STORE size=1024 conc=8 loose=2321 ops/s segments=6814 ops/s x2.94  p99 8.88 -> 3.35 ms (medians of 3 interleaved rounds)
    zeros3_p1c_bench_test.go:84: P1C_STORE size=4096 conc=1 loose=767 ops/s segments=2083 ops/s x2.72  p99 2.62 -> 1.24 ms (medians of 3 interleaved rounds)
    zeros3_p1c_bench_test.go:84: P1C_STORE size=4096 conc=8 loose=2238 ops/s segments=5669 ops/s x2.53  p99 7.15 -> 3.88 ms (medians of 3 interleaved rounds)
    zeros3_p1c_bench_test.go:84: P1C_STORE size=4096 conc=32 loose=2539 ops/s segments=13325 ops/s x5.25  p99 37.42 -> 5.65 ms (medians of 3 interleaved rounds)
    zeros3_p1c_bench_test.go:84: P1C_STORE size=65536 conc=8 loose=1206 ops/s segments=2534 ops/s x2.10  p99 17.86 -> 7.91 ms (medians of 3 interleaved rounds)
    zeros3_p1c_bench_test.go:84: P1C_STORE size=262143 conc=8 loose=637 ops/s segments=877 ops/s x1.38  p99 27.84 -> 18.03 ms (medians of 3 interleaved rounds)
--- PASS: TestP1C_StoreLayerPutThroughput (63.13s)
ok  	zeros3	63.138s
```

## Locator scale (synthetic) (locator_scale.txt)

```
    zeros3_test.go:33780: n=100000 build=22ms heap=5.2MiB (54.6 B/digest) lookup hit=123ns miss=96ns fallback=309ns iterate=83µs
    zeros3_test.go:33780: n=1000000 build=324ms heap=51.7MiB (54.2 B/digest) lookup hit=335ns miss=303ns fallback=603ns iterate=882µs
    zeros3_test.go:33780: n=10000000 build=3.958s heap=522.4MiB (54.8 B/digest) lookup hit=531ns miss=463ns fallback=1001ns iterate=7.23ms
--- PASS: TestLocatorScale (6.31s)
ok  	zeros3	6.354s
```
