## A. Small-file storage (MB): model units (plan, materialized) and allocated store bytes

| family | profile | b8 plan | b8m model | b9 materialized | modeled | realized | survives | b8 alloc | b9 alloc | alloc change | deltas b8/b9 | B/record b8/b9 | verify |
|---|---|---:|---:|---:|---:|---:|---|---:|---:|---:|---|---|---|
| cu-files | archive | 4.04 | 2.46 | 2.45 | -39.0% | -39.4% | yes | 5.26 | 3.64 | -30.8% | 89/525 | 4096/203 | ok |
| cu-files | mixed | 4.24 | 3.62 | 3.60 | -14.7% | -15.2% | yes | 5.46 | 4.82 | -11.6% | 32/76 | 4096/252 | ok |
| go-files | archive | 3.50 | 2.27 | 2.26 | -35.1% | -35.4% | yes | 5.39 | 4.14 | -23.2% | 87/326 | 4096/219 | ok |
| go-files | mixed | 3.77 | 3.27 | 3.25 | -13.3% | -13.7% | yes | 5.66 | 5.14 | -9.1% | 16/61 | 4096/268 | ok |
| synth | archive | 29.06 | 14.61 | 14.35 | -49.7% | -50.6% | yes | 40.90 | 26.16 | -36.0% | 0/8000 | -/239 | ok |
| synth | mixed | 29.06 | 29.06 | 29.06 | -0.0% | -0.0% | n/a | 40.90 | 40.90 | +0.0% | 0/0 | -/- | ok |
| rnd-files | archive | 26.91 | 26.91 | 26.91 | -0.0% | -0.0% | n/a | 28.12 | 28.12 | +0.0% | 0/0 | -/- | ok |
| rnd-files | mixed | 26.91 | 26.91 | 26.91 | -0.0% | -0.0% | n/a | 28.12 | 28.12 | +0.0% | 0/0 | -/- | ok |

## A. Record store (b8 record files vs b9 groups; lookups: page cache dropped, index kept / rebuilt)

| family | lane | records | record bytes | file alloc | groups | group alloc | cold us p50 | open us p50 | warm us p50 | rebuild ms |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| cu-files | b8-archive | 143 | 27483 | 585728 | 0 | 0 | 425 | 414 | 12.003 | 0.14 |
| cu-files | b8-mixed | 51 | 10036 | 208896 | 0 | 0 | 447 | 434 | 15.292 | 0.12 |
| cu-files | b9-archive | 847 | 156779 | 0 | 1 | 172032 | 178 | 935 | 2.87 | 0.79 |
| cu-files | b9-mixed | 130 | 24240 | 0 | 1 | 32768 | 147 | 410 | 2.545 | 0.35 |
| django | b8c-archive | 12 | 22981 | 61440 | 0 | 0 | 699 |  | 16.176 | 0.40 |
| django | b9n-archive | 12 | 22981 | 61440 | 0 | 0 | 551 |  | 13.611 | 0.14 |
| elf | b8c-archive | 24 | 71008 | 126976 | 0 | 0 | 596 |  | 19.287 | 0.33 |
| elf | b9n-archive | 47 | 69674 | 208896 | 0 | 0 | 692 |  | 12.934 | 0.34 |
| go-files | b8-archive | 127 | 25571 | 520192 | 0 | 0 | 549 | 496 | 9.872 | 0.19 |
| go-files | b8-mixed | 24 | 4770 | 98304 | 0 | 0 | 520 | 405 | 9.723 | 0.27 |
| go-files | b9-archive | 506 | 96305 | 0 | 1 | 110592 | 178 | 755 | 2.887 | 0.47 |
| go-files | b9-mixed | 107 | 19452 | 0 | 1 | 28672 | 179 | 508 | 3.111 | 0.43 |
| pe | b8c-archive | 24 | 73789 | 126976 | 0 | 0 | 674 |  | 19.107 | 0.37 |
| pe | b9n-archive | 45 | 73212 | 204800 | 0 | 0 | 684 |  | 12.85 | 0.37 |
| synth | b9-archive | 12000 | 2291979 | 0 | 3 | 2867200 | 180 | 4322 | 3.303 | 5.18 |
| ts | b8c-archive | 6 | 11044 | 32768 | 0 | 0 | 641 |  | 9.388 | 0.22 |
| ts | b9n-archive | 6 | 11044 | 32768 | 0 | 0 | 648 |  | 19.599 | 0.32 |

append grouped=False: 1580 publishes/s (p50 0.55 ms, p99 2.06), 5550080 B written, bodies 4096000 B allocated, retire 0.125 s, compact 0.000 s

append grouped=True: 4232 publishes/s (p50 0.20 ms, p99 0.65), 4354048 B written, bodies 237568 B allocated, retire 0.005 s, compact 0.005 s

## Allocated MB (B: executables, C stores)

| family | cdc | b8c | b9n | b9n / b8c | verify |
|---|---:|---:|---:|---:|---|
| elf | 84.82 | 50.57 | 47.17 | 0.933 | ok |
| pe | 87.35 | 52.63 | 46.67 | 0.887 | ok |
| ts | 12.63 | 5.21 | 5.21 | 1.000 | ok |
| django | 55.66 | 15.70 | 15.70 | 1.000 | ok |

## Reads (median wall ms / CPU ms, every read checked byte for byte)

| family | lane | cold4k | cold64k | cold1m | whole |
|---|---|---:|---:|---:|---:|
| cu-files | b8-archive | 0.28 / 0.18 | 0.72 / 1.01 |  | 0.28 / 0.16 |
| cu-files | b8-mixed | 0.25 / 0.16 | 0.64 / 0.54 |  | 0.27 / 0.16 |
| cu-files | b8g-archive | 0.27 / 0.16 | 0.72 / 0.91 |  | 0.28 / 0.18 |
| cu-files | b9-archive | 0.59 / 0.48 | 0.76 / 0.85 |  | 0.66 / 0.52 |
| cu-files | b9-mixed | 0.26 / 0.17 | 0.91 / 0.88 |  | 0.26 / 0.17 |
| cu-files | cdc | 0.25 / 0.15 | 0.65 / 0.54 |  | 0.28 / 0.15 |
| elf | b8c-archive | 3.01 / 2.65 | 4.76 / 5.18 | 31.40 / 39.96 | 122.55 / 151.19 |
| elf | b9n-archive | 5.54 / 4.89 | 7.75 / 7.17 | 31.90 / 30.13 | 126.25 / 127.92 |
| elf | cdc | 1.23 / 1.13 | 1.74 / 1.63 | 9.65 / 9.63 | 34.33 / 52.43 |
| go-files | b8-archive | 0.31 / 0.20 | 0.44 / 0.32 |  | 0.31 / 0.19 |
| go-files | b9-archive | 0.55 / 0.42 | 0.43 / 0.35 |  | 0.61 / 0.50 |
| go-files | cdc | 0.29 / 0.19 | 0.48 / 0.31 |  | 0.29 / 0.18 |
| pe | b8c-archive | 2.59 / 2.42 | 4.67 / 4.80 | 31.55 / 37.76 | 126.84 / 159.88 |
| pe | b9n-archive | 5.38 / 4.97 | 7.77 / 6.98 | 30.65 / 29.00 | 131.49 / 126.73 |
| pe | cdc | 1.12 / 1.05 | 1.62 / 1.53 | 9.85 / 9.73 | 33.52 / 54.32 |

## Concurrent readers (8, 10 s)

| family | lane | order | reads/s | p50 us | p99 us | CPU s |
|---|---|---|---:|---:|---:|---:|
| cu-files | b8-archive | b8-archive | 70598 | 6 | 2318 | 30.1 |
| cu-files | b9-archive | b8-archive | 16133 | 117 | 4344 | 28.0 |
| cu-files | b9-archive | b9-archive | 15823 | 122 | 4409 | 28.5 |
| cu-files | b8-archive | b9-archive | 61542 | 6 | 2704 | 27.6 |
| cu-files | b8g-archive | b8g-archive | 74009 | 7 | 2310 | 29.4 |
| cu-files | b8-archive | b8g-archive | 68639 | 6 | 2409 | 30.0 |
| cu-files | b8-mixed | b8g-archive | 198305 | 5 | 781 | 32.2 |
| cu-files | b9-mixed | b8g-archive | 84745 | 6 | 2081 | 29.9 |
| cu-files | b9-mixed | b9-mixed | 69434 | 6 | 2742 | 22.9 |
| cu-files | b8-mixed | b9-mixed | 119114 | 5 | 616 | 16.9 |
| cu-files | b8-archive | b9-mixed | 46364 | 6 | 4465 | 20.1 |
| cu-files | b8g-archive | b9-mixed | 52410 | 6 | 3951 | 20.8 |

## C. Decoded-window cache: off vs 64 MiB (mean of two orders); sweep at 8 readers (Zipf)

| family | admission | window KiB | workload | req/s off | req/s on | on/off | p50 ms off/on | p99 ms off/on | hit rate | cache MB |
|---|---|---:|---|---:|---:|---:|---|---|---:|---:|
| ts | first-touch | 64 | cold | 48 | 21 | 0.43 | 19.69/50.09 | 45/89 | 0.11 | 3.9 |
| ts | first-touch | 64 | repeat | 59 | 139 | 2.37 | 16.80/0.08 | 35/75 | 0.88 | 1.0 |
| ts | first-touch | 64 | nearby | 56 | 93 | 1.68 | 17.03/0.10 | 37/74 | 0.81 | 1.7 |
| ts | first-touch | 64 | shared | 66 | 74 | 1.12 | 14.06/0.12 | 23/62 | 0.75 | 0.2 |
| ts | first-touch | 64 | unrelated | 51 | 28 | 0.54 | 18.08/40.22 | 41/118 | 0.32 | 6.0 |
| ts | first-touch | 64 | zipf-8 | 188 | 308 | 1.64 | 34.58/0.08 | 188/211 | 0.79 | 6.2 |
| django | first-touch | 64 | cold | 64 | 30 | 0.47 | 9.48/23.52 | 82/188 | 0.03 | 4.2 |
| django | first-touch | 64 | repeat | 103 | 223 | 2.16 | 4.79/0.09 | 49/76 | 0.88 | 1.1 |
| django | first-touch | 64 | nearby | 99 | 184 | 1.86 | 5.58/0.09 | 48/82 | 0.82 | 1.6 |
| django | first-touch | 64 | shared | 111 | 126 | 1.14 | 8.61/0.09 | 29/64 | 0.75 | 0.4 |
| django | first-touch | 64 | unrelated | 73 | 32 | 0.44 | 7.96/23.09 | 77/115 | 0.08 | 8.0 |
| django | first-touch | 64 | zipf-8 | 242 | 273 | 1.13 | 21.85/0.10 | 162/266 | 0.72 | 8.3 |
| cu-files | first-touch | 64 | cold | 2614 | 3100 | 1.19 | 0.32/0.28 | 2/1 | 0.00 | 0.0 |
| cu-files | first-touch | 64 | repeat | 5931 | 3780 | 0.64 | 0.12/0.13 | 1/1 | 0.00 | 0.0 |
| cu-files | first-touch | 64 | nearby | 5015 | 5657 | 1.13 | 0.12/0.11 | 1/1 | 0.00 | 0.0 |
| cu-files | first-touch | 64 | shared | 8288 | 8435 | 1.02 | 0.07/0.07 | 1/1 | 0.00 | 0.0 |
| cu-files | first-touch | 64 | unrelated | 2547 | 2646 | 1.04 | 0.34/0.32 | 1/1 | 0.00 | 0.0 |
| cu-files | first-touch | 64 | zipf-8 | 16849 | 19041 | 1.13 | 0.07/0.06 | 6/6 | 0.00 | 0.0 |
| ts | second-touch | 64 | cold | 49 | 45 | 0.93 | 19.51/20.04 | 40/72 | 0.29 | 0.3 |
| ts | second-touch | 64 | repeat | 58 | 107 | 1.83 | 16.69/0.07 | 29/70 | 0.86 | 1.0 |
| ts | second-touch | 64 | nearby | 53 | 74 | 1.40 | 17.35/0.12 | 38/66 | 0.78 | 1.6 |
| ts | second-touch | 64 | shared | 67 | 57 | 0.85 | 15.38/13.07 | 20/58 | 0.67 | 0.2 |
| ts | second-touch | 64 | unrelated | 53 | 40 | 0.76 | 17.68/20.61 | 38/79 | 0.21 | 2.2 |
| ts | second-touch | 64 | zipf-8 | 184 | 311 | 1.69 | 35.91/0.09 | 184/188 | 0.83 | 3.9 |
| django | second-touch | 64 | cold | 64 | 62 | 0.96 | 9.84/10.49 | 85/88 | 0.00 | 0.1 |
| django | second-touch | 64 | repeat | 98 | 166 | 1.70 | 5.19/0.10 | 54/64 | 0.86 | 1.1 |
| django | second-touch | 64 | nearby | 100 | 147 | 1.47 | 5.51/0.13 | 51/64 | 0.80 | 1.4 |
| django | second-touch | 64 | shared | 111 | 95 | 0.85 | 8.82/4.22 | 24/54 | 0.67 | 0.4 |
| django | second-touch | 64 | unrelated | 69 | 62 | 0.89 | 8.06/10.38 | 73/79 | 0.00 | 0.7 |
| django | second-touch | 64 | zipf-8 | 242 | 324 | 1.34 | 23.06/0.12 | 168/216 | 0.83 | 3.7 |
| cu-files | second-touch | 64 | cold | 2965 | 3019 | 1.02 | 0.24/0.25 | 2/6 | 0.00 | 0.0 |
| cu-files | second-touch | 64 | repeat | 6637 | 6338 | 0.95 | 0.11/0.10 | 1/1 | 0.00 | 0.0 |
| cu-files | second-touch | 64 | nearby | 5296 | 6250 | 1.18 | 0.13/0.10 | 1/1 | 0.00 | 0.0 |
| cu-files | second-touch | 64 | shared | 9288 | 8204 | 0.88 | 0.06/0.07 | 1/1 | 0.00 | 0.0 |
| cu-files | second-touch | 64 | unrelated | 3422 | 3422 | 1.00 | 0.24/0.25 | 1/1 | 0.00 | 0.0 |
| cu-files | second-touch | 64 | zipf-8 | 22368 | 23113 | 1.03 | 0.05/0.05 | 5/5 | 0.00 | 0.0 |
| ts | second-touch | 16 | cold | 46 | 46 | 1.01 | 20.19/20.58 | 45/45 | 0.00 | 0.0 |
| ts | second-touch | 16 | repeat | 59 | 143 | 2.41 | 16.71/0.07 | 31/58 | 0.86 | 0.3 |
| ts | second-touch | 16 | nearby | 55 | 67 | 1.21 | 16.33/16.14 | 44/51 | 0.57 | 0.7 |
| ts | second-touch | 16 | shared | 74 | 93 | 1.25 | 12.64/13.27 | 22/26 | 0.67 | 0.0 |
| ts | second-touch | 16 | unrelated | 52 | 49 | 0.94 | 18.02/18.82 | 37/46 | 0.00 | 0.2 |
| ts | second-touch | 16 | zipf-8 | 192 | 304 | 1.58 | 34.57/0.10 | 165/232 | 0.80 | 1.4 |
| django | second-touch | 16 | cold | 66 | 57 | 0.87 | 8.09/9.57 | 87/82 | 0.00 | 0.0 |
| django | second-touch | 16 | repeat | 101 | 198 | 1.96 | 4.93/0.07 | 49/65 | 0.86 | 0.3 |
| django | second-touch | 16 | nearby | 100 | 105 | 1.05 | 5.51/4.54 | 50/57 | 0.56 | 0.7 |
| django | second-touch | 16 | shared | 123 | 110 | 0.89 | 7.92/4.28 | 24/48 | 0.67 | 0.1 |
| django | second-touch | 16 | unrelated | 68 | 66 | 0.98 | 8.85/7.95 | 80/77 | 0.00 | 0.1 |
| django | second-touch | 16 | zipf-8 | 231 | 362 | 1.57 | 23.81/0.11 | 179/182 | 0.79 | 1.4 |
sweep ts first-touch window 64 KiB budget 16 MiB: 339 req/s, hit rate 0.79
sweep ts first-touch window 64 KiB budget 256 MiB: 301 req/s, hit rate 0.79
sweep django first-touch window 64 KiB budget 16 MiB: 270 req/s, hit rate 0.72
sweep django first-touch window 64 KiB budget 256 MiB: 295 req/s, hit rate 0.72
sweep cu-files first-touch window 64 KiB budget 16 MiB: 19141 req/s, hit rate 0.00
sweep cu-files first-touch window 64 KiB budget 256 MiB: 22991 req/s, hit rate 0.00
sweep ts second-touch window 64 KiB budget 16 MiB: 296 req/s, hit rate 0.83
sweep ts second-touch window 64 KiB budget 256 MiB: 305 req/s, hit rate 0.83
sweep django second-touch window 64 KiB budget 16 MiB: 331 req/s, hit rate 0.83
sweep django second-touch window 64 KiB budget 256 MiB: 349 req/s, hit rate 0.83
sweep cu-files second-touch window 64 KiB budget 16 MiB: 19493 req/s, hit rate 0.00
sweep cu-files second-touch window 64 KiB budget 256 MiB: 26381 req/s, hit rate 0.00
sweep ts second-touch window 16 KiB budget 16 MiB: 325 req/s, hit rate 0.80
sweep ts second-touch window 16 KiB budget 256 MiB: 327 req/s, hit rate 0.80
sweep django second-touch window 16 KiB budget 16 MiB: 379 req/s, hit rate 0.79
sweep django second-touch window 16 KiB budget 256 MiB: 395 req/s, hit rate 0.79
