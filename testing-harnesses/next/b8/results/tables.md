
## Allocated store bytes (MB) after optimize + gc + repack + compact, archive

| family | cdc | b7 | b7r | b7rg | b8s | b8 | b8-nodelta | b8-nozip | b8 / pre-B8 best | b8 / b7rg | verify | plan = model |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|---|
| ts |  | 12.63 | 12.63 | 9.32 | 5.21 | 5.21 | 9.30 |  | 0.412 | 0.558 | True | True |
| ts-mix | 12.75 | 12.75 | 9.70 | 9.70 | 6.89 | 6.89 | 9.05 |  | 0.710 | 0.710 | True | True |
| django | 55.66 | 41.02 | 41.02 | 34.44 | 15.70 | 15.70 | 38.96 | 23.03 | 0.383 | 0.456 | True | True |
| wrap | 51.77 | 51.77 | 51.78 | 44.60 | 26.85 | 26.85 | 31.98 |  | 0.519 | 0.602 | True | True |
| cu-gz | 44.77 | 41.21 | 41.21 | 41.21 | 22.20 | 22.20 | 41.21 |  | 0.539 | 0.539 | True | True |
| random | 50.45 | 50.45 | 50.45 |  |  | 50.45 |  |  | 1.000 |  | True | True |
| lone | 6.24 | 6.24 | 6.24 |  |  | 6.22 |  |  | 0.996 |  | True | True |
| zst19 | 13.48 | 13.48 | 13.48 |  |  | 13.48 |  |  | 1.000 |  | True | True |

## Allocated store bytes (MB) after optimize + gc + repack + compact, mixed

| family | cdc | b7 | b7r | b7rg | b8s | b8 | b8 / pre-B8 best | b8 / b7rg | verify | plan = model |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---|---|
| ts |  | 12.63 | 12.63 | 12.63 | 9.04 | 9.04 | 0.716 | 0.716 | True | True |
| ts-mix | 12.75 | 12.75 | 9.70 | 9.70 | 8.80 | 8.80 | 0.907 | 0.907 | True | True |
| django | 55.66 | 41.07 | 41.07 | 41.07 | 32.82 | 32.82 | 0.799 | 0.799 | True | True |
| wrap | 51.77 | 51.77 | 51.77 | 51.77 | 26.85 | 26.85 | 0.519 | 0.519 | True | True |
| cu-gz | 44.77 | 41.21 | 41.21 | 41.21 | 41.21 | 41.21 | 1.000 | 1.000 | True | True |
| random | 50.45 | 50.45 |  |  |  | 50.45 | 1.000 |  | True | True |
| lone | 6.24 | 6.24 |  |  |  | 6.24 | 1.000 |  | True | True |
| zst19 | 13.48 | 13.48 |  |  |  | 13.48 | 1.000 |  | True | True |

## Allocated store bytes (MB) after optimize + gc + repack + compact, hot

| family | cdc | b7rg | b8 | b8 / pre-B8 best | b8 / b7rg | verify | plan = model |
|---|---:|---:|---:|---:|---:|---|---|
| ts |  | 12.63 | 12.63 |  | 1.000 | True | True |
| django | 55.66 | 55.66 | 55.66 |  | 1.000 | True | True |

## Canonical analysis (b8 archive)

| family | containers | rejected | streams | shared | plain MB | recipe MB | analysis s | planned deltas / reps / pins / cdc |
|---|---:|---:|---:|---:|---:|---:|---:|---|
| ts | 3 | 0 | 3 | 0 | 67.92 | 0.63 | 6.0 | 2 / 3 / 1 / 0 |
| ts-mix | 2 | 0 | 2 | 0 | 45.38 | 2.14 | 5.8 | 2 / 2 / 1 / 0 |
| django | 6 | 0 | 6 | 0 | 253.85 | 2.92 | 24.2 | 5 / 6 / 1 / 0 |
| wrap | 3 | 0 | 1 | 2 | 62.73 | 10.37 | 25.0 | 1 / 3 / 1 / 0 |
| cu-gz | 3 | 0 | 3 | 0 | 187.02 | 4.06 | 34.3 | 2 / 3 / 1 / 0 |
| random | 0 | 0 | 0 | 0 | 0.00 | 0.00 | 0.0 | 0 / 0 / 0 / 3 |
| lone | 2 | 0 | 2 | 0 | 0.86 | 0.01 | 0.1 | 1 / 2 / 1 / 1 |
| zst19 | 2 | 2 | 0 | 0 | 0.00 | 0.00 | 7.0 | 0 / 0 / 0 / 2 |

## Migration cost (archive)

| family | lane | wall s | CPU s | written MB | peak allocated MB | RSS MiB | reclaim s |
|---|---|---:|---:|---:|---:|---:|---:|
| ts | b7-archive | 1.5 | 1.6 | 0.00 | 12.63 | 128 | 0.1 |
| ts | b7r-archive | 6.4 | 16.2 | 67.92 | 12.63 | 178 | 0.1 |
| ts | b7rg-archive | 15.9 | 30.3 | 136.27 | 35.78 | 181 | 0.5 |
| ts | b8s-archive | 19.1 | 38.5 | 161.46 | 35.82 | 296 | 0.4 |
| ts | b8-archive | 18.2 | 38.5 | 161.14 | 35.82 | 300 | 0.4 |
| ts | b8-nodelta-archive | 14.0 | 30.0 | 139.41 | 36.56 | 234 | 0.4 |
| ts-mix | b7-archive | 0.2 | 0.4 | 0.00 | 12.75 | 75 | 0.4 |
| ts-mix | b7r-archive | 9.3 | 16.1 | 75.51 | 16.80 | 244 | 0.5 |
| ts-mix | b7rg-archive | 9.4 | 16.6 | 75.50 | 16.80 | 260 | 0.5 |
| ts-mix | b8s-archive | 16.2 | 23.4 | 102.29 | 16.80 | 320 | 0.5 |
| ts-mix | b8-archive | 17.2 | 24.3 | 102.21 | 16.80 | 321 | 0.5 |
| ts-mix | b8-nodelta-archive | 9.0 | 15.6 | 72.99 | 14.16 | 282 | 0.4 |
| django | b7-archive | 9.2 | 9.7 | 1.22 | 56.59 | 262 | 0.2 |
| django | b7r-archive | 21.6 | 36.5 | 185.61 | 56.59 | 391 | 0.2 |
| django | b7rg-archive | 39.8 | 76.9 | 444.67 | 131.72 | 379 | 2.6 |
| wrap | b7-archive | 0.8 | 1.4 | 0.00 | 51.77 | 120 | 0.6 |
| wrap | b7r-archive | 29.5 | 36.3 | 204.96 | 51.77 | 361 | 0.7 |
| wrap | b7rg-archive | 53.4 | 73.7 | 389.52 | 102.01 | 359 | 1.8 |
| wrap | b8s-archive | 76.3 | 98.7 | 429.56 | 70.68 | 809 | 1.2 |
| wrap | b8-archive | 75.6 | 97.2 | 429.42 | 70.68 | 828 | 1.2 |
| wrap | b8-nodelta-archive | 49.4 | 71.4 | 348.86 | 61.40 | 286 | 0.9 |
| cu-gz | b7-archive | 10.4 | 10.8 | 14.26 | 56.25 | 274 | 0.3 |
| cu-gz | b7r-archive | 37.5 | 63.2 | 201.34 | 56.25 | 369 | 0.4 |
| cu-gz | b7rg-archive | 38.0 | 64.3 | 201.55 | 56.25 | 334 | 0.4 |
| cu-gz | b8s-archive | 124.0 | 191.6 | 449.39 | 113.77 | 924 | 1.4 |
| cu-gz | b8-archive | 138.0 | 185.3 | 449.41 | 113.77 | 904 | 1.4 |
| cu-gz | b8-nodelta-archive | 34.8 | 60.8 | 201.66 | 56.26 | 332 | 0.3 |
| django | b8s-archive | 100.0 | 232.5 | 581.75 | 122.82 | 1129 | 0.9 |
| django | b8-archive | 98.6 | 228.5 | 581.51 | 122.82 | 1188 | 0.9 |
| django | b8-nodelta-archive | 52.9 | 142.3 | 304.82 | 80.82 | 716 | 0.5 |
| django | b8-nozip-archive | 48.2 | 91.1 | 439.92 | 121.45 | 989 | 0.9 |
| random | b7-archive | 0.2 | 0.5 | 0.00 | 50.45 | 87 | 0.2 |
| random | b7r-archive | 0.3 | 0.5 | 0.00 | 50.45 | 89 | 0.2 |
| random | b8-archive | 0.2 | 0.5 | 0.00 | 50.45 | 87 | 0.2 |
| lone | b7-archive | 0.1 | 0.1 | 0.00 | 6.24 | 144 | 0.0 |
| lone | b7r-archive | 0.1 | 0.1 | 0.00 | 6.24 | 39 | 0.1 |
| lone | b8-archive | 0.4 | 1.0 | 2.56 | 6.95 | 86 | 0.1 |
| zst19 | b7-archive | 0.1 | 0.2 | 0.00 | 13.48 | 47 | 0.1 |
| zst19 | b7r-archive | 7.0 | 7.1 | 140.45 | 13.48 | 254 | 0.1 |
| zst19 | b8-archive | 7.1 | 7.2 | 140.43 | 13.48 | 262 | 0.1 |

## Cold reads through the production reader (medians; model = optimizer read model)

| family | lane | mode | n | wall ms | CPU ms | device reads | model ms | forms |
|---|---|---|---:|---:|---:|---:|---:|---|
| cu-gz | b7rg-archive | cold1m | 6 | 2.49 | 4.39 | 18 | 5.21 | keep-pin:4, keep-delta:2 |
| cu-gz | b7rg-archive | cold4k | 24 | 0.86 | 0.69 | 2 | 0.72 | keep-pin:16, keep-delta:8 |
| cu-gz | b7rg-archive | cold64k | 12 | 1.09 | 0.94 | 3 | 0.95 | keep-pin:8, keep-delta:4 |
| cu-gz | b7rg-archive | warm4k | 3 | 0.43 | 0.46 | 1 | 0.89 | keep-pin:2, keep-delta:1 |
| cu-gz | b7rg-archive | whole | 6 | 78.21 | 119.86 | 205 | 81.42 | keep-pin:4, keep-delta:2 |
| cu-gz | b7rg-mixed | cold1m | 6 | 2.43 | 4.34 | 18 | 5.21 | keep-pin:4, keep-delta:2 |
| cu-gz | b7rg-mixed | cold4k | 24 | 0.88 | 0.71 | 2 | 0.72 | keep-pin:16, keep-delta:8 |
| cu-gz | b7rg-mixed | cold64k | 12 | 1.12 | 0.76 | 3 | 0.95 | keep-pin:8, keep-delta:4 |
| cu-gz | b7rg-mixed | warm4k | 3 | 0.44 | 0.27 | 1 | 0.89 | keep-pin:2, keep-delta:1 |
| cu-gz | b7rg-mixed | whole | 6 | 53.28 | 96.53 | 206 | 81.42 | keep-pin:4, keep-delta:2 |
| cu-gz | b8-archive | cold1m | 6 | 211.27 | 579.87 | 125 | 256.28 | keep-rep:6 |
| cu-gz | b8-archive | cold4k | 24 | 30.70 | 29.92 | 35 | 33.26 | keep-rep:24 |
| cu-gz | b8-archive | cold64k | 12 | 72.59 | 69.18 | 59 | 62.38 | keep-rep:12 |
| cu-gz | b8-archive | warm4k | 3 | 19.43 | 19.31 | 4 | 47.94 | keep-rep:3 |
| cu-gz | b8-archive | whole | 6 | 2157.58 | 7191.71 | 536 | 2205.11 | keep-rep:6 |
| cu-gz | b8-mixed | cold1m | 6 | 2.79 | 4.74 | 20 | 5.21 | keep-pin:4, keep-delta:2 |
| cu-gz | b8-mixed | cold4k | 24 | 0.92 | 0.66 | 2 | 0.72 | keep-pin:16, keep-delta:8 |
| cu-gz | b8-mixed | cold64k | 12 | 0.97 | 0.85 | 3 | 0.95 | keep-pin:8, keep-delta:4 |
| cu-gz | b8-mixed | warm4k | 3 | 0.25 | 0.31 | 0 | 0.89 | keep-pin:2, keep-delta:1 |
| cu-gz | b8-mixed | whole | 6 | 85.18 | 120.93 | 208 | 81.42 | keep-pin:4, keep-delta:2 |
| cu-gz | cdc | cold1m | 6 | 3.05 | 2.61 | 1 | 4.89 | cdc:6 |
| cu-gz | cdc | cold4k | 24 | 0.40 | 0.25 | 1 | 0.46 | cdc:24 |
| cu-gz | cdc | cold64k | 12 | 0.49 | 0.30 | 1 | 0.63 | cdc:12 |
| cu-gz | cdc | warm4k | 3 | 0.22 | 0.23 | 1 | 0.52 | cdc:3 |
| cu-gz | cdc | whole | 6 | 38.86 | 65.95 | 17 | 76.32 | cdc:6 |
| django | b7rg-archive | cold1m | 12 | 89.38 | 253.07 | 44 | 146.39 | keep-rep:6, keep-delta:4, keep-pin:2 |
| django | b7rg-archive | cold4k | 48 | 12.63 | 12.63 | 8 | 8.94 | keep-rep:24, keep-delta:16, keep-pin:8 |
| django | b7rg-archive | cold64k | 24 | 29.88 | 28.49 | 14 | 14.85 | keep-rep:12, keep-delta:8, keep-pin:4 |
| django | b7rg-archive | warm4k | 6 | 6.55 | 6.25 | 2 | 9.06 | keep-rep:3, keep-delta:2, keep-pin:1 |
| django | b7rg-archive | whole | 12 | 671.48 | 2041.47 | 138 | 3706.37 | keep-rep:6, keep-delta:4, keep-pin:2 |
| django | b7rg-mixed | cold1m | 12 | 3.26 | 3.98 | 8 | 5.09 | cdc:6, keep-delta:4, keep-pin:2 |
| django | b7rg-mixed | cold4k | 48 | 0.58 | 0.41 | 2 | 0.62 | cdc:24, keep-delta:16, keep-pin:8 |
| django | b7rg-mixed | cold64k | 24 | 0.68 | 0.56 | 2 | 0.85 | cdc:12, keep-delta:8, keep-pin:4 |
| django | b7rg-mixed | warm4k | 6 | 0.18 | 0.20 | 0 | 0.59 | cdc:3, keep-delta:2, keep-pin:1 |
| django | b7rg-mixed | whole | 12 | 34.91 | 51.85 | 52 | 55.24 | cdc:6, keep-delta:4, keep-pin:2 |
| django | b8-archive | cold1m | 12 | 212.69 | 558.54 | 67 | 201.81 | keep-rep:12 |
| django | b8-archive | cold4k | 48 | 18.75 | 19.87 | 21 | 19.60 | keep-rep:48 |
| django | b8-archive | cold64k | 24 | 50.02 | 71.86 | 26 | 29.39 | keep-rep:24 |
| django | b8-archive | warm4k | 6 | 15.22 | 15.67 | 0 | 19.29 | keep-rep:6 |
| django | b8-archive | whole | 12 | 1379.31 | 4245.87 | 166 | 1363.25 | keep-rep:12 |
| django | b8-mixed | cold1m | 12 | 7.09 | 8.64 | 20 | 7.18 | keep-rep:4, keep-delta:4, keep-pin:2, cdc:2 |
| django | b8-mixed | cold4k | 48 | 0.96 | 0.70 | 5 | 1.20 | keep-rep:16, keep-delta:16, keep-pin:8, cdc:8 |
| django | b8-mixed | cold64k | 24 | 1.28 | 1.29 | 6 | 1.68 | keep-rep:8, keep-delta:8, keep-pin:4, cdc:4 |
| django | b8-mixed | warm4k | 6 | 0.57 | 0.44 | 1 | 1.07 | keep-rep:2, keep-delta:2, keep-pin:1, cdc:1 |
| django | b8-mixed | whole | 12 | 46.84 | 54.84 | 97 | 109.46 | keep-rep:4, keep-delta:4, keep-pin:2, cdc:2 |
| django | cdc | cold1m | 12 | 3.24 | 2.81 | 2 | 4.83 | cdc:12 |
| django | cdc | cold4k | 48 | 0.41 | 0.24 | 1 | 0.44 | cdc:48 |
| django | cdc | cold64k | 24 | 0.56 | 0.35 | 1 | 0.78 | cdc:24 |
| django | cdc | warm4k | 6 | 0.28 | 0.18 | 1 | 0.45 | cdc:6 |
| django | cdc | whole | 12 | 20.73 | 37.09 | 11 | 51.04 | cdc:12 |
| ts | b7rg-archive | cold1m | 6 | 191.99 | 581.43 | 46 | 176.37 | cdc:2, keep-rep:4 |
| ts | b7rg-archive | cold4k | 24 | 21.84 | 21.95 | 10 | 16.53 | cdc:8, keep-rep:16 |
| ts | b7rg-archive | cold64k | 12 | 44.29 | 45.51 | 10 | 25.92 | cdc:4, keep-rep:8 |
| ts | b7rg-archive | warm4k | 3 | 25.18 | 26.52 | 1 | 16.53 | cdc:1, keep-rep:2 |
| ts | b7rg-archive | whole | 6 | 715.60 | 2074.35 | 54 | 876.85 | cdc:2, keep-rep:4 |
| ts | b7rg-mixed | cold1m | 6 | 2.95 | 2.76 | 2 | 4.63 | cdc:6 |
| ts | b7rg-mixed | cold4k | 24 | 0.39 | 0.24 | 1 | 0.50 | cdc:24 |
| ts | b7rg-mixed | cold64k | 12 | 0.52 | 0.31 | 1 | 0.71 | cdc:12 |
| ts | b7rg-mixed | warm4k | 3 | 0.40 | 0.32 | 1 | 0.55 | cdc:3 |
| ts | b7rg-mixed | whole | 6 | 13.40 | 13.79 | 6 | 22.26 | cdc:6 |
| ts | b8-archive | cold1m | 6 | 228.87 | 677.88 | 56 | 180.30 | keep-rep:6 |
| ts | b8-archive | cold4k | 24 | 31.28 | 31.32 | 14 | 19.65 | keep-rep:24 |
| ts | b8-archive | cold64k | 12 | 67.12 | 64.89 | 16 | 29.70 | keep-rep:12 |
| ts | b8-archive | warm4k | 3 | 36.94 | 37.22 | 9 | 20.03 | keep-rep:3 |
| ts | b8-archive | whole | 6 | 782.25 | 2546.92 | 88 | 634.83 | keep-rep:6 |
| ts | b8-mixed | cold1m | 6 | 167.21 | 564.44 | 44 | 178.33 | cdc:2, keep-rep:4 |
| ts | b8-mixed | cold4k | 24 | 19.03 | 19.05 | 10 | 18.90 | cdc:8, keep-rep:16 |
| ts | b8-mixed | cold64k | 12 | 44.14 | 43.84 | 12 | 28.28 | cdc:4, keep-rep:8 |
| ts | b8-mixed | warm4k | 3 | 21.50 | 21.07 | 0 | 19.62 | cdc:1, keep-rep:2 |
| ts | b8-mixed | whole | 6 | 723.15 | 2402.74 | 88 | 607.77 | cdc:2, keep-rep:4 |
| ts | cdc | cold1m | 6 | 3.22 | 2.36 | 2 | 4.63 | cdc:6 |
| ts | cdc | cold4k | 24 | 0.41 | 0.27 | 1 | 0.50 | cdc:24 |
| ts | cdc | cold64k | 12 | 0.56 | 0.35 | 1 | 0.71 | cdc:12 |
| ts | cdc | warm4k | 3 | 0.58 | 0.29 | 1 | 0.55 | cdc:3 |
| ts | cdc | whole | 6 | 18.52 | 18.95 | 6 | 22.26 | cdc:6 |
| wrap | b7rg-archive | cold1m | 8 | 145.95 | 195.39 | 28 | 88.64 | keep-rep:4, cdc:4 |
| wrap | b7rg-archive | cold4k | 32 | 6.99 | 7.29 | 6 | 9.14 | keep-rep:16, cdc:16 |
| wrap | b7rg-archive | cold64k | 16 | 21.68 | 20.05 | 8 | 13.63 | keep-rep:8, cdc:8 |
| wrap | b7rg-archive | warm4k | 4 | 6.05 | 5.65 | 1 | 9.81 | keep-rep:2, cdc:2 |
| wrap | b7rg-archive | whole | 8 | 750.60 | 2027.49 | 161 | 2128.77 | keep-rep:4, cdc:4 |
| wrap | b7rg-mixed | cold1m | 8 | 3.33 | 3.08 | 1 | 4.90 | cdc:8 |
| wrap | b7rg-mixed | cold4k | 32 | 0.41 | 0.28 | 1 | 0.49 | cdc:32 |
| wrap | b7rg-mixed | cold64k | 16 | 0.62 | 0.43 | 1 | 0.90 | cdc:16 |
| wrap | b7rg-mixed | warm4k | 4 | 0.61 | 0.35 | 0 | 0.61 | cdc:4 |
| wrap | b7rg-mixed | whole | 8 | 30.41 | 43.23 | 14 | 67.99 | cdc:8 |
| wrap | b8-archive | cold1m | 8 | 210.51 | 391.25 | 55 | 229.75 | keep-rep:6, keep-pin:2 |
| wrap | b8-archive | cold4k | 32 | 50.05 | 29.35 | 15 | 43.10 | keep-rep:24, keep-pin:8 |
| wrap | b8-archive | cold64k | 16 | 62.00 | 52.05 | 18 | 54.21 | keep-rep:12, keep-pin:4 |
| wrap | b8-archive | warm4k | 4 | 30.98 | 31.09 | 8 | 43.12 | keep-rep:3, keep-pin:1 |
| wrap | b8-archive | whole | 8 | 1084.19 | 3358.84 | 279 | 1472.04 | keep-rep:6, keep-pin:2 |
| wrap | b8-mixed | cold1m | 8 | 178.77 | 353.44 | 52 | 229.75 | keep-rep:6, keep-pin:2 |
| wrap | b8-mixed | cold4k | 32 | 29.95 | 33.08 | 15 | 43.10 | keep-rep:24, keep-pin:8 |
| wrap | b8-mixed | cold64k | 16 | 51.73 | 51.62 | 18 | 54.21 | keep-rep:12, keep-pin:4 |
| wrap | b8-mixed | warm4k | 4 | 30.47 | 31.02 | 0 | 43.12 | keep-rep:3, keep-pin:1 |
| wrap | b8-mixed | whole | 8 | 1080.57 | 3385.62 | 280 | 1472.04 | keep-rep:6, keep-pin:2 |
| wrap | cdc | cold1m | 8 | 2.88 | 2.81 | 1 | 4.90 | cdc:8 |
| wrap | cdc | cold4k | 32 | 0.43 | 0.25 | 1 | 0.49 | cdc:32 |
| wrap | cdc | cold64k | 16 | 0.63 | 0.43 | 1 | 0.90 | cdc:16 |
| wrap | cdc | warm4k | 4 | 0.57 | 0.46 | 1 | 0.61 | cdc:4 |
| wrap | cdc | whole | 8 | 34.16 | 50.68 | 16 | 67.99 | cdc:8 |

## Concurrent readers

| family | lane | order | reads/s | p50 µs | p95 µs | p99 µs | CPU s |
|---|---|---|---:|---:|---:|---:|---:|
| ts | cdc | cdc | 205185 | 7 | 53 | 443 | 33.8 |
| ts | b7rg-archive | cdc | 188 | 32001 | 143329 | 200237 | 39.1 |
| ts | b8-archive | cdc | 151 | 42410 | 135161 | 188354 | 39.3 |
| ts | b8-archive | b8-archive | 155 | 41673 | 127363 | 170093 | 39.4 |
| ts | b7rg-archive | b8-archive | 184 | 35187 | 137662 | 189019 | 39.1 |
| ts | cdc | b8-archive | 208531 | 7 | 53 | 486 | 33.9 |
| django | cdc | cdc | 50361 | 36 | 268 | 1913 | 36.9 |
| django | b7rg-archive | cdc | 278 | 14347 | 103899 | 162863 | 38.6 |
| django | b8-archive | cdc | 182 | 30215 | 127674 | 208762 | 38.7 |
| django | b8-archive | b8-archive | 183 | 31349 | 131866 | 201956 | 39.0 |
| django | b7rg-archive | b8-archive | 268 | 13866 | 106980 | 167573 | 38.8 |
| django | cdc | b8-archive | 51008 | 36 | 264 | 1905 | 37.3 |

## Scale (synthetic histories, planning only)

| candidate_s | canon_s | cdc_mb | containers | cpu_s | largest_partition | nodes | options | partitions | planned_mb | releases | rss_mib | search_s | streams | wall_s |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 9.005 | 0.811 | 3.742 | 11 | 10.16 | 22 | 22 | 192 | 1 | 0.713 | 8 | 244.883 | 0.009 | 11 | 9.024 |
| 37.589 | 2.995 | 13.344 | 43 | 41.685 | 86 | 86 | 1078 | 1 | 1.813 | 32 | 448.82 | 0.162 | 43 | 37.776 |
| 127.721 | 9.731 | 39.143 | 128 | 140.915 | 256 | 256 | 3489 | 1 | 5.112 | 96 | 990.266 | 0.625 | 128 | 128.465 |
