# C1 benchmark tables

## Loopback request samples

Median [minimum, maximum] across repetitions. Rates count verified operations.
GET timing includes body read and both SHA-256 computations. The LIST lane has 20 calls per run.

| System | Operation | Clients | Runs | Operations/s | p50 ms | p95 ms | p99 ms |
|---|---|---:|---:|---:|---:|---:|---:|
| garage | delete | 1 | 3 | 159.64 [53.90, 335.70] | 2.81 [2.36, 3.57] | 31.98 [5.69, 98.18] | 48.89 [10.42, 189.76] |
| garage | delete | 4 | 3 | 386.31 [356.04, 462.65] | 7.69 [7.38, 8.99] | 25.31 [19.44, 28.68] | 31.62 [23.10, 36.75] |
| garage | delete | 8 | 3 | 327.56 [62.01, 352.12] | 15.03 [14.54, 27.82] | 72.66 [43.89, 844.05] | 98.74 [98.15, 1070.15] |
| garage | get-random | 1 | 3 | 61.36 [60.70, 61.45] | 4.07 [4.00, 4.13] | 44.02 [43.99, 44.04] | 46.41 [44.76, 47.60] |
| garage | get-random | 4 | 3 | 223.90 [210.96, 231.08] | 4.83 [4.68, 6.04] | 47.08 [44.76, 47.64] | 48.68 [48.17, 52.74] |
| garage | get-random | 8 | 3 | 410.33 [403.06, 414.83] | 6.08 [5.23, 7.12] | 47.93 [46.18, 47.98] | 50.49 [48.48, 50.65] |
| garage | get-sequential | 1 | 3 | 59.96 [59.14, 61.07] | 4.26 [3.96, 4.28] | 44.24 [44.22, 44.42] | 48.01 [45.59, 56.05] |
| garage | get-sequential | 4 | 3 | 231.76 [217.46, 231.79] | 4.92 [4.39, 6.14] | 46.28 [45.63, 47.08] | 49.48 [48.40, 52.34] |
| garage | get-sequential | 8 | 3 | 439.52 [422.82, 442.27] | 4.86 [4.82, 5.78] | 46.57 [44.76, 47.15] | 48.03 [47.01, 50.97] |
| garage | head | 1 | 3 | 1090.96 [925.42, 1098.23] | 0.86 [0.86, 1.08] | 1.06 [1.04, 1.15] | 1.19 [1.10, 1.28] |
| garage | head | 4 | 3 | 1068.72 [1061.31, 1105.38] | 2.82 [2.79, 2.94] | 7.90 [7.35, 8.26] | 10.94 [9.98, 13.36] |
| garage | head | 8 | 3 | 1120.28 [1079.60, 1165.85] | 4.98 [4.86, 5.65] | 16.41 [14.72, 18.95] | 25.15 [20.53, 27.61] |
| garage | list | 1 | 3 | 117.24 [96.45, 124.15] | 7.93 [7.84, 7.96] | 8.50 [8.40, 9.26] | — |
| garage | list | 4 | 3 | 139.24 [117.99, 139.35] | 20.86 [19.64, 27.43] | 56.45 [55.86, 59.32] | — |
| garage | list | 8 | 3 | 116.72 [107.58, 137.17] | 54.20 [36.63, 57.55] | 120.73 [113.00, 133.39] | — |
| garage | mixed | 1 | 3 | 72.82 [70.17, 74.32] | 4.80 [4.76, 5.08] | 44.07 [44.06, 44.48] | 44.96 [44.65, 46.22] |
| garage | mixed | 4 | 3 | 259.16 [247.66, 273.21] | 5.92 [5.53, 6.19] | 45.58 [44.98, 47.36] | 49.20 [46.80, 49.63] |
| garage | mixed | 8 | 3 | 435.79 [390.78, 450.82] | 9.65 [9.24, 10.68] | 46.77 [46.59, 49.07] | 50.25 [48.78, 59.90] |
| garage | put | 1 | 3 | 46.61 [39.02, 75.74] | 6.56 [6.55, 7.50] | 114.00 [26.15, 145.97] | 180.08 [128.29, 193.81] |
| garage | put | 4 | 3 | 188.45 [146.49, 199.72] | 20.48 [19.64, 22.62] | 37.25 [31.74, 58.13] | 65.88 [33.04, 73.26] |
| garage | put | 8 | 3 | 112.62 [16.47, 196.95] | 43.17 [41.96, 81.98] | 244.12 [58.04, 1557.74] | 331.78 [73.50, 1854.01] |
| garage | range | 1 | 3 | 27.12 [27.11, 27.14] | 43.95 [43.94, 43.96] | 44.57 [44.12, 45.04] | 47.96 [47.94, 49.78] |
| garage | range | 4 | 3 | 105.98 [105.80, 108.19] | 43.97 [43.96, 43.97] | 48.02 [47.17, 48.21] | 48.85 [48.81, 50.27] |
| garage | range | 8 | 3 | 209.56 [207.28, 212.29] | 43.96 [43.93, 44.01] | 48.00 [45.92, 48.20] | 49.39 [48.00, 49.51] |
| minio | delete | 1 | 3 | 755.04 [733.15, 762.72] | 1.27 [1.26, 1.27] | 1.63 [1.54, 1.79] | 1.99 [1.68, 2.35] |
| minio | delete | 4 | 3 | 1080.79 [1053.97, 1157.93] | 3.06 [2.89, 3.12] | 7.62 [6.58, 7.95] | 11.28 [10.46, 11.41] |
| minio | delete | 8 | 3 | 1138.79 [1110.82, 1150.24] | 5.24 [5.05, 5.28] | 16.01 [13.19, 16.31] | 20.24 [19.31, 25.77] |
| minio | get-random | 1 | 3 | 559.15 [553.75, 571.60] | 1.44 [1.42, 1.45] | 3.13 [3.05, 3.56] | 3.69 [3.64, 3.80] |
| minio | get-random | 4 | 3 | 792.26 [756.84, 885.55] | 3.98 [3.49, 3.98] | 11.69 [9.17, 11.82] | 13.14 [9.63, 14.39] |
| minio | get-random | 8 | 3 | 865.30 [857.33, 891.51] | 7.88 [7.22, 7.95] | 19.02 [18.47, 20.40] | 24.39 [20.83, 26.40] |
| minio | get-sequential | 1 | 3 | 549.25 [526.74, 615.92] | 1.49 [1.31, 1.55] | 3.20 [2.87, 3.66] | 3.41 [3.02, 4.56] |
| minio | get-sequential | 4 | 3 | 822.49 [813.50, 905.83] | 4.00 [3.58, 4.11] | 10.26 [9.64, 10.98] | 14.77 [11.79, 16.54] |
| minio | get-sequential | 8 | 3 | 851.32 [743.37, 856.94] | 7.86 [7.12, 8.23] | 20.33 [18.75, 27.19] | 26.42 [25.16, 37.30] |
| minio | head | 1 | 3 | 751.36 [387.34, 798.57] | 1.24 [1.21, 1.35] | 1.59 [1.41, 1.86] | 2.89 [1.90, 55.59] |
| minio | head | 4 | 3 | 999.54 [969.10, 1001.68] | 3.19 [3.00, 3.26] | 7.99 [7.83, 9.72] | 11.03 [10.79, 17.47] |
| minio | head | 8 | 3 | 1022.90 [929.17, 1061.71] | 6.08 [6.07, 6.23] | 17.66 [17.01, 20.99] | 26.22 [23.00, 34.37] |
| minio | list | 1 | 3 | 93.50 [90.85, 93.61] | 10.02 [9.69, 10.33] | 11.95 [11.77, 13.15] | — |
| minio | list | 4 | 3 | 130.89 [65.10, 139.15] | 30.04 [25.16, 30.86] | 50.49 [41.61, 163.96] | — |
| minio | list | 8 | 3 | 135.90 [69.25, 141.75] | 52.78 [42.83, 62.47] | 113.39 [86.77, 179.67] | — |
| minio | mixed | 1 | 3 | 418.79 [271.91, 471.73] | 1.77 [1.63, 1.92] | 5.04 [4.22, 6.56] | 6.07 [5.60, 60.09] |
| minio | mixed | 4 | 3 | 685.00 [684.32, 792.74] | 4.92 [4.19, 4.95] | 11.43 [10.10, 13.09] | 14.59 [13.59, 17.34] |
| minio | mixed | 8 | 3 | 760.68 [454.60, 782.96] | 8.59 [8.16, 8.59] | 22.80 [20.91, 67.94] | 32.63 [27.63, 104.08] |
| minio | put | 1 | 3 | 228.86 [160.89, 297.44] | 3.09 [2.39, 3.22] | 9.51 [7.03, 15.20] | 11.18 [8.07, 70.65] |
| minio | put | 4 | 3 | 512.72 [269.90, 560.35] | 7.40 [6.45, 7.60] | 13.09 [11.63, 55.88] | 15.67 [13.00, 109.78] |
| minio | put | 8 | 3 | 600.17 [576.54, 659.61] | 11.93 [11.10, 13.04] | 21.77 [20.33, 22.40] | 25.43 [23.26, 28.30] |
| minio | range | 1 | 3 | 656.51 [656.27, 697.27] | 1.35 [1.32, 1.37] | 2.04 [1.87, 2.33] | 4.17 [2.06, 4.68] |
| minio | range | 4 | 3 | 926.41 [344.56, 999.93] | 3.30 [3.14, 4.18] | 10.86 [8.65, 47.28] | 15.02 [10.82, 144.60] |
| minio | range | 8 | 3 | 956.58 [464.06, 1011.89] | 6.19 [5.92, 7.79] | 20.33 [16.42, 62.66] | 27.52 [24.61, 104.60] |
| rustfs | delete | 1 | 3 | 255.62 [180.31, 257.86] | 3.65 [3.61, 3.66] | 5.32 [5.04, 11.90] | 8.26 [6.44, 17.93] |
| rustfs | delete | 4 | 3 | 541.61 [380.95, 564.45] | 7.04 [6.83, 7.92] | 10.46 [9.64, 27.54] | 11.95 [10.74, 33.85] |
| rustfs | delete | 8 | 3 | 678.76 [171.84, 690.20] | 10.66 [10.51, 13.50] | 19.01 [17.77, 163.95] | 21.54 [21.34, 251.01] |
| rustfs | get-random | 1 | 3 | 591.76 [590.03, 660.54] | 1.11 [1.09, 1.18] | 3.45 [3.23, 3.97] | 5.80 [3.47, 5.89] |
| rustfs | get-random | 4 | 3 | 906.41 [835.95, 966.27] | 3.33 [3.20, 3.55] | 10.14 [8.97, 10.52] | 13.25 [12.16, 13.46] |
| rustfs | get-random | 8 | 3 | 946.39 [793.81, 955.65] | 7.02 [5.89, 7.58] | 19.08 [16.36, 24.69] | 23.60 [20.54, 32.77] |
| rustfs | get-sequential | 1 | 3 | 563.84 [500.15, 578.47] | 1.24 [1.23, 1.34] | 3.70 [3.63, 4.06] | 5.69 [4.69, 8.15] |
| rustfs | get-sequential | 4 | 3 | 842.07 [784.47, 889.36] | 3.75 [3.73, 3.78] | 10.07 [8.84, 10.27] | 12.85 [11.10, 22.16] |
| rustfs | get-sequential | 8 | 3 | 867.04 [830.00, 926.19] | 7.37 [6.34, 8.08] | 19.92 [19.12, 20.67] | 25.19 [23.86, 25.85] |
| rustfs | head | 1 | 3 | 1027.13 [958.93, 1037.80] | 0.92 [0.89, 1.05] | 1.10 [1.00, 1.14] | 1.23 [1.17, 1.90] |
| rustfs | head | 4 | 3 | 1050.66 [861.05, 1135.37] | 2.81 [2.62, 3.68] | 8.83 [7.71, 10.48] | 12.84 [10.24, 15.72] |
| rustfs | head | 8 | 3 | 1104.47 [1038.18, 1147.63] | 5.61 [4.57, 5.62] | 17.56 [15.60, 18.44] | 22.33 [20.25, 26.68] |
| rustfs | list | 1 | 3 | 111.50 [107.85, 115.39] | 8.60 [8.60, 8.81] | 9.66 [8.93, 11.83] | — |
| rustfs | list | 4 | 3 | 130.00 [111.26, 141.25] | 25.88 [24.88, 30.06] | 43.61 [41.12, 44.68] | — |
| rustfs | list | 8 | 3 | 132.98 [128.78, 134.61] | 41.66 [39.14, 43.74] | 89.64 [86.77, 109.34] | — |
| rustfs | mixed | 1 | 3 | 420.28 [403.04, 450.31] | 1.35 [1.34, 1.61] | 5.68 [5.56, 5.94] | 6.43 [6.17, 6.91] |
| rustfs | mixed | 4 | 3 | 631.71 [525.50, 736.61] | 4.76 [4.38, 5.26] | 14.67 [13.33, 21.59] | 20.40 [17.69, 34.71] |
| rustfs | mixed | 8 | 3 | 698.66 [398.02, 804.68] | 8.38 [4.18, 9.50] | 28.35 [20.73, 82.87] | 29.71 [29.48, 112.32] |
| rustfs | put | 1 | 3 | 234.25 [224.65, 237.92] | 2.73 [2.59, 2.95] | 10.40 [9.25, 10.54] | 11.87 [11.59, 13.08] |
| rustfs | put | 4 | 3 | 423.49 [413.27, 472.32] | 7.19 [7.06, 7.41] | 19.76 [16.93, 20.35] | 21.74 [19.22, 30.48] |
| rustfs | put | 8 | 3 | 376.47 [51.60, 486.47] | 17.75 [13.35, 18.63] | 43.66 [30.68, 2369.05] | 60.33 [42.15, 2403.89] |
| rustfs | range | 1 | 3 | 669.78 [662.92, 803.36] | 1.08 [1.07, 1.24] | 2.21 [1.87, 4.10] | 4.38 [2.75, 5.65] |
| rustfs | range | 4 | 3 | 943.16 [856.37, 1025.52] | 3.57 [3.08, 3.96] | 8.57 [7.22, 9.61] | 12.07 [11.89, 15.53] |
| rustfs | range | 8 | 3 | 1039.86 [975.49, 1052.87] | 6.02 [5.61, 6.70] | 16.70 [15.05, 17.36] | 23.65 [23.19, 30.12] |
| seaweed | delete | 1 | 3 | 887.76 [586.00, 928.40] | 1.11 [1.04, 1.18] | 1.25 [1.19, 2.16] | 1.36 [1.33, 22.49] |
| seaweed | delete | 4 | 3 | 1212.55 [1078.15, 1301.56] | 2.74 [2.43, 3.06] | 6.66 [5.42, 7.64] | 9.38 [8.26, 10.18] |
| seaweed | delete | 8 | 3 | 1313.99 [1038.62, 1363.13] | 4.76 [4.51, 5.74] | 14.91 [11.13, 17.15] | 19.08 [17.51, 28.04] |
| seaweed | get-random | 1 | 3 | 505.23 [333.45, 512.29] | 1.67 [1.59, 1.97] | 3.40 [3.35, 5.15] | 4.33 [3.70, 22.82] |
| seaweed | get-random | 4 | 3 | 842.60 [796.57, 865.71] | 3.98 [3.77, 4.25] | 10.00 [9.60, 10.64] | 13.69 [11.58, 14.29] |
| seaweed | get-random | 8 | 3 | 835.67 [776.69, 881.28] | 7.63 [6.82, 7.85] | 20.59 [19.31, 21.70] | 27.42 [26.41, 28.83] |
| seaweed | get-sequential | 1 | 3 | 437.66 [408.46, 556.87] | 1.72 [1.51, 1.92] | 4.03 [3.14, 4.23] | 5.27 [3.29, 7.69] |
| seaweed | get-sequential | 4 | 3 | 841.05 [826.30, 859.25] | 4.09 [3.87, 4.27] | 8.74 [8.70, 9.80] | 10.95 [10.30, 13.86] |
| seaweed | get-sequential | 8 | 3 | 839.74 [805.00, 861.73] | 8.02 [7.02, 8.09] | 19.58 [18.30, 21.87] | 27.75 [26.47, 33.58] |
| seaweed | head | 1 | 3 | 758.10 [649.75, 897.88] | 1.28 [1.07, 1.34] | 1.46 [1.24, 1.60] | 1.69 [1.48, 2.93] |
| seaweed | head | 4 | 3 | 958.66 [949.05, 963.70] | 3.20 [3.06, 3.27] | 8.80 [8.11, 9.76] | 13.02 [10.74, 13.68] |
| seaweed | head | 8 | 3 | 1084.48 [994.00, 1107.69] | 5.57 [5.29, 6.45] | 17.33 [16.58, 17.71] | 23.02 [23.00, 23.57] |
| seaweed | list | 1 | 3 | 103.30 [87.22, 119.39] | 8.62 [8.26, 10.22] | 10.73 [9.06, 15.33] | — |
| seaweed | list | 4 | 3 | 142.79 [127.64, 143.89] | 27.08 [24.13, 27.35] | 44.00 [39.23, 52.15] | — |
| seaweed | list | 8 | 3 | 145.37 [144.96, 148.70] | 41.31 [37.28, 41.53] | 74.92 [73.04, 84.80] | — |
| seaweed | mixed | 1 | 3 | 434.56 [355.17, 477.26] | 1.74 [1.72, 1.95] | 4.31 [3.90, 5.21] | 4.92 [4.39, 18.11] |
| seaweed | mixed | 4 | 3 | 782.16 [734.94, 827.80] | 4.38 [4.23, 4.63] | 10.69 [9.46, 12.04] | 12.44 [11.86, 12.93] |
| seaweed | mixed | 8 | 3 | 793.12 [735.24, 865.74] | 8.26 [7.79, 9.04] | 20.64 [18.02, 24.12] | 27.42 [21.90, 29.75] |
| seaweed | put | 1 | 3 | 188.57 [178.15, 265.36] | 2.77 [2.20, 2.90] | 17.73 [9.88, 20.71] | 21.74 [13.84, 23.47] |
| seaweed | put | 4 | 3 | 471.49 [342.34, 482.39] | 6.89 [6.64, 8.23] | 18.67 [16.83, 27.36] | 24.30 [20.26, 36.30] |
| seaweed | put | 8 | 3 | 341.45 [314.67, 421.86] | 22.37 [15.63, 23.52] | 40.56 [39.85, 43.02] | 50.47 [47.26, 52.19] |
| seaweed | range | 1 | 3 | 601.45 [542.67, 633.09] | 1.57 [1.54, 1.80] | 2.22 [1.80, 2.32] | 2.49 [1.95, 2.67] |
| seaweed | range | 4 | 3 | 914.90 [776.95, 947.96] | 3.35 [3.17, 3.37] | 9.76 [9.56, 10.15] | 14.40 [11.87, 17.86] |
| seaweed | range | 8 | 3 | 1049.61 [983.01, 1056.94] | 5.45 [5.29, 6.23] | 18.16 [16.91, 18.80] | 24.93 [22.43, 26.23] |
| seaweed (8 volumes) | delete | 1 | 3 | 788.88 [779.05, 856.68] | 1.22 [1.08, 1.23] | 1.53 [1.44, 1.54] | 1.82 [1.55, 1.91] |
| seaweed (8 volumes) | delete | 4 | 3 | 1102.81 [1075.46, 1114.40] | 2.91 [2.68, 2.98] | 7.06 [6.76, 8.28] | 12.14 [9.96, 14.26] |
| seaweed (8 volumes) | delete | 8 | 3 | 1106.76 [867.36, 1230.79] | 5.58 [4.62, 7.09] | 17.17 [16.92, 18.88] | 25.38 [21.76, 25.92] |
| seaweed (8 volumes) | get-random | 1 | 3 | 406.87 [380.08, 500.63] | 1.99 [1.72, 2.21] | 4.45 [3.57, 4.84] | 5.69 [3.96, 8.73] |
| seaweed (8 volumes) | get-random | 4 | 3 | 818.77 [638.42, 819.55] | 4.19 [4.10, 5.11] | 9.81 [9.50, 12.57] | 15.57 [12.82, 24.04] |
| seaweed (8 volumes) | get-random | 8 | 3 | 824.02 [760.61, 904.65] | 8.09 [7.42, 8.72] | 22.12 [17.60, 22.20] | 29.23 [27.51, 30.39] |
| seaweed (8 volumes) | get-sequential | 1 | 3 | 409.54 [275.13, 502.60] | 1.93 [1.63, 2.71] | 4.29 [3.63, 6.23] | 6.00 [4.13, 8.20] |
| seaweed (8 volumes) | get-sequential | 4 | 3 | 735.41 [723.75, 807.88] | 4.47 [4.10, 4.60] | 11.40 [10.02, 11.56] | 13.67 [12.63, 13.73] |
| seaweed (8 volumes) | get-sequential | 8 | 3 | 696.90 [580.94, 763.61] | 8.98 [8.00, 9.28] | 26.29 [22.92, 69.86] | 36.36 [33.58, 83.93] |
| seaweed (8 volumes) | head | 1 | 3 | 759.53 [704.79, 763.45] | 1.27 [1.26, 1.37] | 1.52 [1.43, 1.63] | 1.83 [1.71, 2.57] |
| seaweed (8 volumes) | head | 4 | 3 | 973.80 [939.00, 985.15] | 3.31 [3.29, 3.32] | 8.61 [8.59, 8.86] | 12.02 [11.28, 14.90] |
| seaweed (8 volumes) | head | 8 | 3 | 868.37 [587.65, 1008.96] | 6.19 [5.56, 7.38] | 29.42 [18.55, 73.16] | 39.18 [24.24, 93.87] |
| seaweed (8 volumes) | list | 1 | 3 | 72.04 [51.41, 79.73] | 11.28 [9.27, 16.74] | 36.49 [18.51, 37.06] | — |
| seaweed (8 volumes) | list | 4 | 3 | 134.97 [120.71, 141.61] | 25.39 [24.76, 27.80] | 45.65 [42.48, 51.18] | — |
| seaweed (8 volumes) | list | 8 | 3 | 127.74 [92.50, 145.04] | 53.37 [44.87, 65.33] | 86.98 [69.82, 136.12] | — |
| seaweed (8 volumes) | mixed | 1 | 3 | 395.50 [362.70, 426.46] | 1.86 [1.86, 2.27] | 5.17 [4.95, 5.24] | 6.49 [5.62, 6.79] |
| seaweed (8 volumes) | mixed | 4 | 3 | 700.92 [689.48, 712.96] | 4.54 [4.40, 4.78] | 12.53 [11.76, 13.40] | 16.74 [14.77, 19.07] |
| seaweed (8 volumes) | mixed | 8 | 3 | 715.79 [668.16, 722.37] | 9.60 [8.62, 10.22] | 22.94 [19.64, 23.43] | 28.74 [28.58, 34.83] |
| seaweed (8 volumes) | put | 1 | 3 | 184.69 [137.11, 233.59] | 2.79 [2.58, 4.06] | 19.90 [12.69, 25.59] | 21.42 [13.50, 29.56] |
| seaweed (8 volumes) | put | 4 | 3 | 439.61 [397.41, 447.01] | 7.36 [6.58, 8.37] | 19.34 [17.23, 20.09] | 28.58 [23.88, 29.13] |
| seaweed (8 volumes) | put | 8 | 3 | 373.95 [302.94, 434.57] | 18.45 [17.15, 21.45] | 42.98 [30.94, 74.63] | 49.53 [48.61, 89.17] |
| seaweed (8 volumes) | range | 1 | 3 | 600.41 [527.97, 606.36] | 1.57 [1.57, 1.86] | 2.08 [1.97, 2.26] | 2.52 [2.36, 3.79] |
| seaweed (8 volumes) | range | 4 | 3 | 830.63 [795.45, 985.01] | 3.60 [3.29, 3.79] | 8.85 [8.58, 9.86] | 12.60 [11.84, 13.49] |
| seaweed (8 volumes) | range | 8 | 3 | 944.05 [843.14, 994.22] | 7.36 [5.87, 7.65] | 18.39 [16.52, 20.70] | 23.88 [22.60, 30.63] |
| zeros3 | delete | 1 | 3 | 883.11 [841.31, 883.31] | 1.10 [1.08, 1.12] | 1.36 [1.34, 1.40] | 1.59 [1.51, 1.77] |
| zeros3 | delete | 4 | 3 | 1070.90 [1032.86, 1208.76] | 3.27 [2.82, 3.28] | 7.16 [6.72, 8.20] | 9.88 [9.87, 10.00] |
| zeros3 | delete | 8 | 3 | 1167.03 [1078.99, 1198.32] | 5.00 [4.28, 5.76] | 15.63 [14.69, 16.18] | 26.92 [19.15, 27.22] |
| zeros3 | get-random | 1 | 3 | 705.61 [512.14, 706.86] | 1.09 [1.06, 1.11] | 2.83 [2.65, 5.57] | 5.75 [3.54, 9.71] |
| zeros3 | get-random | 4 | 3 | 991.43 [806.26, 997.42] | 3.57 [3.50, 3.68] | 8.68 [7.91, 10.95] | 11.95 [10.59, 14.04] |
| zeros3 | get-random | 8 | 3 | 903.83 [901.67, 1017.14] | 7.66 [6.51, 8.05] | 17.72 [15.45, 19.43] | 20.50 [17.34, 28.94] |
| zeros3 | get-sequential | 1 | 3 | 687.47 [670.97, 757.95] | 1.06 [1.00, 1.16] | 2.91 [2.86, 3.12] | 3.46 [3.29, 4.29] |
| zeros3 | get-sequential | 4 | 3 | 892.76 [860.89, 921.78] | 3.47 [3.38, 3.49] | 9.89 [9.88, 10.89] | 13.49 [11.84, 13.58] |
| zeros3 | get-sequential | 8 | 3 | 953.83 [935.20, 966.79] | 6.97 [6.30, 7.27] | 18.53 [17.47, 22.67] | 25.56 [21.35, 33.43] |
| zeros3 | head | 1 | 3 | 1073.42 [983.14, 1170.60] | 0.90 [0.82, 0.98] | 1.04 [0.90, 1.15] | 1.12 [1.10, 1.82] |
| zeros3 | head | 4 | 3 | 1043.52 [1008.23, 1065.56] | 2.91 [2.72, 2.99] | 8.82 [8.13, 9.88] | 15.40 [12.05, 19.09] |
| zeros3 | head | 8 | 3 | 1140.36 [974.56, 1166.85] | 4.96 [4.78, 5.68] | 16.77 [16.08, 19.96] | 28.86 [25.85, 29.65] |
| zeros3 | list | 1 | 3 | 128.02 [127.87, 142.52] | 6.90 [6.82, 6.94] | 7.46 [7.42, 8.16] | — |
| zeros3 | list | 4 | 3 | 124.27 [111.74, 145.27] | 28.74 [20.25, 30.17] | 52.55 [48.37, 64.93] | — |
| zeros3 | list | 8 | 3 | 139.82 [120.08, 147.82] | 43.67 [42.62, 47.41] | 87.81 [69.38, 107.42] | — |
| zeros3 | mixed | 1 | 3 | 604.02 [583.49, 608.37] | 1.27 [1.16, 1.35] | 3.09 [2.84, 3.23] | 3.82 [3.53, 4.00] |
| zeros3 | mixed | 4 | 3 | 830.43 [809.09, 833.41] | 4.21 [4.15, 4.24] | 9.95 [9.01, 10.28] | 11.88 [11.76, 13.25] |
| zeros3 | mixed | 8 | 3 | 863.38 [860.19, 899.34] | 7.60 [6.58, 7.65] | 18.81 [17.88, 22.28] | 31.90 [27.22, 32.43] |
| zeros3 | put | 1 | 3 | 375.01 [160.24, 414.50] | 1.75 [1.64, 2.21] | 6.04 [6.03, 22.51] | 6.66 [6.34, 69.97] |
| zeros3 | put | 4 | 3 | 600.53 [410.70, 627.38] | 6.01 [5.64, 7.73] | 11.57 [11.15, 19.59] | 13.34 [13.25, 32.56] |
| zeros3 | put | 8 | 3 | 687.87 [492.56, 690.16] | 10.71 [10.35, 12.53] | 19.68 [19.50, 32.16] | 22.80 [21.51, 38.87] |
| zeros3 | range | 1 | 3 | 1091.08 [917.35, 1103.09] | 0.88 [0.87, 1.05] | 1.00 [0.97, 1.19] | 1.26 [1.14, 1.51] |
| zeros3 | range | 4 | 3 | 1045.66 [1021.33, 1056.83] | 2.90 [2.81, 3.12] | 8.94 [8.53, 9.68] | 12.87 [12.57, 13.48] |
| zeros3 | range | 8 | 3 | 1038.25 [1034.30, 1072.43] | 5.51 [5.50, 5.86] | 18.44 [16.06, 19.49] | 24.72 [19.85, 29.28] |

## Three-revision history samples

Allocation uses 512-byte stat block counts, including directories and indexes.
Backup timing includes CLI startup and destination writes. S3 restore is a verified in-memory GET.

| Family | System | Profile | Runs | Logical MiB | Pre-maint. MiB | Retained MiB | Ingest s | Maint. s | Restore s |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|
| combined | borg | snapshot | 3 | 89.80 [89.80, 89.80] | 38.59 [37.07, 47.10] | 38.55 [37.03, 47.06] | 2.49 [2.48, 2.78] | 0.64 [0.62, 0.70] | 2.57 [2.50, 2.57] |
| combined | garage | plain | 3 | 89.80 [89.80, 89.80] | 47.23 [47.23, 47.24] | 47.23 [47.23, 47.24] | 8.96 [4.28, 9.27] | — | 19.18 [19.04, 19.39] |
| combined | kopia | snapshot | 3 | 89.80 [89.80, 89.80] | 58.50 [58.50, 58.50] | 58.50 [58.50, 58.51] | 0.89 [0.84, 0.93] | 0.20 [0.19, 0.22] | 1.00 [0.93, 1.02] |
| combined | kopia | zstd-fastest | 3 | 89.80 [89.80, 89.80] | 44.18 [44.18, 44.18] | 44.19 [44.19, 44.19] | 0.77 [0.76, 0.98] | 0.20 [0.19, 0.20] | 1.02 [0.90, 1.06] |
| combined | minio | plain | 3 | 89.80 [89.80, 89.80] | 96.09 [96.09, 96.09] | 96.09 [96.09, 96.09] | 3.11 [2.91, 3.15] | — | 1.33 [1.31, 1.38] |
| combined | restic | snapshot | 3 | 89.80 [89.80, 89.80] | 43.09 [37.60, 46.82] | 43.09 [37.60, 46.82] | 2.57 [2.52, 2.58] | 0.71 [0.69, 0.71] | 2.35 [2.31, 2.37] |
| combined | rustfs | plain | 3 | 89.80 [89.80, 89.80] | 96.18 [96.18, 96.18] | 96.18 [96.18, 96.18] | 3.09 [3.06, 3.35] | — | 1.20 [1.18, 1.24] |
| combined | seaweed | plain | 3 | 89.80 [89.80, 89.80] | 82.92 [82.92, 82.93] | 82.92 [82.92, 82.93] | 11.93 [11.27, 11.94] | — | 1.44 [1.36, 1.51] |
| combined | zeros3 | archive | 3 | 89.80 [89.80, 89.80] | 40.35 [40.35, 40.35] | 35.57 [35.57, 35.57] | 1.92 [1.73, 2.09] | 11.77 [11.70, 11.97] | 1.25 [1.12, 1.26] |
| combined | zeros3 | hot | 3 | 89.80 [89.80, 89.80] | 40.35 [40.35, 40.35] | 40.41 [40.41, 40.41] | 1.87 [1.77, 1.90] | 10.22 [10.07, 10.52] | 1.01 [1.00, 1.14] |
| combined | zeros3 | mixed | 3 | 89.80 [89.80, 89.80] | 40.35 [40.35, 40.35] | 35.94 [35.93, 35.94] | 1.81 [1.77, 1.87] | 11.57 [11.03, 11.68] | 1.16 [1.16, 1.20] |
| combined | zeros3 | plain | 3 | 89.80 [89.80, 89.80] | 40.35 [40.35, 40.35] | 40.35 [40.35, 40.35] | 1.82 [1.78, 1.86] | — | 1.04 [0.96, 1.08] |
| combined | zeros3 | structured | 3 | 89.80 [89.80, 89.80] | 52.77 [52.77, 52.77] | 52.77 [52.77, 52.77] | 2.26 [2.21, 2.34] | — | 0.98 [0.97, 1.08] |
| coreutils | zeros3 | archive | 1 | 1.55 [1.55, 1.55] | 1.63 [1.63, 1.63] | 1.79 [1.79, 1.79] | 0.05 [0.05, 0.05] | 0.49 [0.49, 0.49] | 0.03 [0.03, 0.03] |
| coreutils | zeros3 | archive-delta | 1 | 1.55 [1.55, 1.55] | 1.63 [1.63, 1.63] | 1.79 [1.79, 1.79] | 0.05 [0.05, 0.05] | 0.43 [0.43, 0.43] | 0.03 [0.03, 0.03] |
| coreutils | zeros3 | plain | 1 | 1.55 [1.55, 1.55] | 1.63 [1.63, 1.63] | 1.63 [1.63, 1.63] | 0.05 [0.05, 0.05] | — | 0.02 [0.02, 0.02] |
| entropy | zeros3 | archive | 1 | 12.00 [12.00, 12.00] | 12.12 [12.12, 12.12] | 12.12 [12.12, 12.12] | 0.10 [0.10, 0.10] | 0.18 [0.18, 0.18] | 0.03 [0.03, 0.03] |
| entropy | zeros3 | archive-delta | 1 | 12.00 [12.00, 12.00] | 12.12 [12.12, 12.12] | 12.12 [12.12, 12.12] | 0.08 [0.08, 0.08] | 0.17 [0.17, 0.17] | 0.04 [0.04, 0.04] |
| entropy | zeros3 | plain | 1 | 12.00 [12.00, 12.00] | 12.12 [12.12, 12.12] | 12.12 [12.12, 12.12] | 0.09 [0.09, 0.09] | — | 0.04 [0.04, 0.04] |
| lodash | zeros3 | archive | 1 | 0.91 [0.91, 0.91] | 0.98 [0.98, 0.98] | 4.48 [4.48, 4.48] | 0.02 [0.02, 0.02] | 2.22 [2.22, 2.22] | 0.31 [0.31, 0.31] |
| lodash | zeros3 | archive-delta | 1 | 0.91 [0.91, 0.91] | 0.98 [0.98, 0.98] | 0.99 [0.99, 0.99] | 0.06 [0.06, 0.06] | 0.38 [0.38, 0.38] | 0.01 [0.01, 0.01] |
| lodash | zeros3 | plain | 1 | 0.91 [0.91, 0.91] | 0.98 [0.98, 0.98] | 0.98 [0.98, 0.98] | 0.02 [0.02, 0.02] | — | 0.01 [0.01, 0.01] |
| oci | zeros3 | archive | 1 | 4.69 [4.69, 4.69] | 1.84 [1.84, 1.84] | 1.84 [1.84, 1.84] | 0.05 [0.05, 0.05] | 3.39 [3.39, 3.39] | 0.02 [0.02, 0.02] |
| oci | zeros3 | archive-delta | 1 | 4.69 [4.69, 4.69] | 1.84 [1.84, 1.84] | 1.96 [1.96, 1.96] | 0.05 [0.05, 0.05] | 0.48 [0.48, 0.48] | 0.02 [0.02, 0.02] |
| oci | zeros3 | plain | 1 | 4.69 [4.69, 4.69] | 1.84 [1.84, 1.84] | 1.84 [1.84, 1.84] | 0.05 [0.05, 0.05] | — | 0.02 [0.02, 0.02] |
| package | zeros3 | archive | 1 | 4.69 [4.69, 4.69] | 1.84 [1.84, 1.84] | 1.84 [1.84, 1.84] | 0.05 [0.05, 0.05] | 1.83 [1.83, 1.83] | 0.02 [0.02, 0.02] |
| package | zeros3 | archive-delta | 1 | 4.69 [4.69, 4.69] | 1.84 [1.84, 1.84] | 1.96 [1.96, 1.96] | 0.04 [0.04, 0.04] | 0.43 [0.43, 0.43] | 0.02 [0.02, 0.02] |
| package | zeros3 | plain | 1 | 4.69 [4.69, 4.69] | 1.84 [1.84, 1.84] | 1.84 [1.84, 1.84] | 0.04 [0.04, 0.04] | — | 0.02 [0.02, 0.02] |
| parquet | borg | snapshot | 1 | 5.54 [5.54, 5.54] | 3.66 [3.66, 3.66] | 3.62 [3.62, 3.62] | 2.21 [2.21, 2.21] | 0.69 [0.69, 0.69] | 2.07 [2.07, 2.07] |
| parquet | kopia | snapshot | 1 | 5.54 [5.54, 5.54] | 5.73 [5.73, 5.73] | 5.73 [5.73, 5.73] | 0.68 [0.68, 0.68] | 0.23 [0.23, 0.23] | 0.61 [0.61, 0.61] |
| parquet | restic | snapshot | 1 | 5.54 [5.54, 5.54] | 1.84 [1.84, 1.84] | 1.84 [1.84, 1.84] | 2.23 [2.23, 2.23] | 0.70 [0.70, 0.70] | 2.17 [2.17, 2.17] |
| parquet | zeros3 | archive | 1 | 5.54 [5.54, 5.54] | 2.09 [2.09, 2.09] | 2.50 [2.50, 2.50] | 0.04 [0.04, 0.04] | 2.35 [2.35, 2.35] | 0.24 [0.24, 0.24] |
| parquet | zeros3 | archive-delta | 1 | 5.54 [5.54, 5.54] | 2.09 [2.09, 2.09] | 1.92 [1.92, 1.92] | 0.04 [0.04, 0.04] | 0.38 [0.38, 0.38] | 0.02 [0.02, 0.02] |
| parquet | zeros3 | plain | 1 | 5.54 [5.54, 5.54] | 2.09 [2.09, 2.09] | 2.09 [2.09, 2.09] | 0.04 [0.04, 0.04] | — | 0.02 [0.02, 0.02] |
| shift | zeros3 | archive | 1 | 12.02 [12.02, 12.02] | 4.48 [4.48, 4.48] | 4.34 [4.34, 4.34] | 0.08 [0.08, 0.08] | 0.59 [0.59, 0.59] | 0.04 [0.04, 0.04] |
| shift | zeros3 | archive-delta | 1 | 12.02 [12.02, 12.02] | 4.48 [4.48, 4.48] | 4.34 [4.34, 4.34] | 0.08 [0.08, 0.08] | 0.58 [0.58, 0.58] | 0.07 [0.07, 0.07] |
| shift | zeros3 | plain | 1 | 12.02 [12.02, 12.02] | 4.48 [4.48, 4.48] | 4.48 [4.48, 4.48] | 0.08 [0.08, 0.08] | — | 0.04 [0.04, 0.04] |
| sst | borg | snapshot | 1 | 10.69 [10.69, 10.69] | 10.33 [10.33, 10.33] | 10.29 [10.29, 10.29] | 2.23 [2.23, 2.23] | 0.64 [0.64, 0.64] | 2.20 [2.20, 2.20] |
| sst | kopia | snapshot | 1 | 10.69 [10.69, 10.69] | 10.88 [10.88, 10.88] | 10.89 [10.89, 10.89] | 0.63 [0.63, 0.63] | 0.19 [0.19, 0.19] | 0.59 [0.59, 0.59] |
| sst | restic | snapshot | 1 | 10.69 [10.69, 10.69] | 6.24 [6.24, 6.24] | 6.24 [6.24, 6.24] | 2.30 [2.30, 2.30] | 0.69 [0.69, 0.69] | 2.15 [2.15, 2.15] |
| sst | zeros3 | archive | 1 | 10.69 [10.69, 10.69] | 10.81 [10.81, 10.81] | 4.34 [4.34, 4.34] | 0.08 [0.08, 0.08] | 11.46 [11.46, 11.46] | 0.10 [0.10, 0.10] |
| sst | zeros3 | archive-delta | 1 | 10.69 [10.69, 10.69] | 10.81 [10.81, 10.81] | 4.34 [4.34, 4.34] | 0.08 [0.08, 0.08] | 2.35 [2.35, 2.35] | 0.06 [0.06, 0.06] |
| sst | zeros3 | plain | 1 | 10.69 [10.69, 10.69] | 10.81 [10.81, 10.81] | 10.81 [10.81, 10.81] | 0.08 [0.08, 0.08] | — | 0.03 [0.03, 0.03] |
| tensor | zeros3 | archive | 1 | 12.00 [12.00, 12.00] | 2.34 [2.34, 2.34] | 2.11 [2.11, 2.11] | 0.07 [0.07, 0.07] | 0.52 [0.52, 0.52] | 0.06 [0.06, 0.06] |
| tensor | zeros3 | archive-delta | 1 | 12.00 [12.00, 12.00] | 2.34 [2.34, 2.34] | 2.34 [2.34, 2.34] | 0.08 [0.08, 0.08] | 0.12 [0.12, 0.12] | 0.03 [0.03, 0.03] |
| tensor | zeros3 | plain | 1 | 12.00 [12.00, 12.00] | 2.34 [2.34, 2.34] | 2.34 [2.34, 2.34] | 0.07 [0.07, 0.07] | — | 0.03 [0.03, 0.03] |

## OpenXet loopback

Interface counters include protocol overhead and other traffic on the measured interface.
They do not report CAS allocation or exact per-request payload bytes.

| Resource | Operation | Case | Status | s | Logical MiB | TX KiB | RX KiB |
|---|---|---|---|---:|---:|---:|---:|
| dataset | create | — | verified | 0.018 | 0.000 | 1.0 | 1.0 |
| dataset | upload | new | verified | 0.146 | 4.000 | 4341.0 | 4341.0 |
| dataset | upload | exact | verified | 0.034 | 4.000 | 7.0 | 7.0 |
| dataset | upload | edit | verified | 0.082 | 4.008 | 95.5 | 95.5 |
| dataset | upload | shift | verified | 0.085 | 4.016 | 103.6 | 103.6 |
| dataset | upload | tensor | verified | 0.098 | 4.000 | 778.4 | 778.4 |
| dataset | upload | tensor-edit | verified | 0.082 | 4.000 | 80.3 | 80.3 |
| dataset | upload | entropy | verified | 0.088 | 4.000 | 4114.9 | 4114.9 |
| dataset | upload | entropy-replace | verified | 0.090 | 4.000 | 4116.9 | 4116.9 |
| dataset | metadata | new | verified | 0.007 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | new | verified | 0.069 | 4.000 | 4108.5 | 4108.5 |
| dataset | download-cached | new | verified | 0.006 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | new | verified | 0.025 | 4.000 | 4107.1 | 4107.1 |
| dataset | metadata | exact | verified | 0.004 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | exact | verified | 0.020 | 4.000 | 4109.2 | 4109.2 |
| dataset | download-cached | exact | verified | 0.006 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | exact | verified | 0.018 | 4.000 | 4236.0 | 4236.0 |
| dataset | metadata | edit | verified | 0.005 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | edit | verified | 0.024 | 4.008 | 4121.1 | 4121.1 |
| dataset | download-cached | edit | verified | 0.006 | 4.008 | 0.0 | 0.0 |
| dataset | download-forced | edit | verified | 0.022 | 4.008 | 4120.0 | 4120.0 |
| dataset | metadata | shift | verified | 0.005 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | shift | verified | 0.020 | 4.016 | 4128.6 | 4128.6 |
| dataset | download-cached | shift | verified | 0.005 | 4.016 | 0.0 | 0.0 |
| dataset | download-forced | shift | verified | 0.020 | 4.016 | 4126.9 | 4126.9 |
| dataset | metadata | tensor | verified | 0.006 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | tensor | verified | 0.041 | 4.000 | 790.4 | 790.4 |
| dataset | download-cached | tensor | verified | 0.013 | 4.000 | 0.1 | 0.1 |
| dataset | download-forced | tensor | verified | 0.019 | 4.000 | 789.8 | 789.8 |
| dataset | metadata | tensor-edit | verified | 0.004 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | tensor-edit | error | 0.055 | 4.000 | 86.8 | 86.8 |
| dataset | download-cached | tensor-edit | error | 0.011 | 4.000 | 85.5 | 85.5 |
| dataset | download-forced | tensor-edit | error | 0.009 | 4.000 | 82.9 | 82.9 |
| dataset | metadata | entropy | verified | 0.004 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | entropy | verified | 0.021 | 4.000 | 4108.9 | 4108.9 |
| dataset | download-cached | entropy | verified | 0.006 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | entropy | verified | 0.023 | 4.000 | 4111.3 | 4111.3 |
| dataset | metadata | entropy-replace | verified | 0.008 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | entropy-replace | verified | 0.023 | 4.000 | 4108.8 | 4108.8 |
| dataset | download-cached | entropy-replace | verified | 0.005 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | entropy-replace | verified | 0.035 | 4.000 | 4107.3 | 4107.3 |

## OpenXet loopback repeat

Interface counters include protocol overhead and other traffic on the measured interface.
They do not report CAS allocation or exact per-request payload bytes.

| Resource | Operation | Case | Status | s | Logical MiB | TX KiB | RX KiB |
|---|---|---|---|---:|---:|---:|---:|
| dataset | create | — | verified | 0.018 | 0.000 | 1.0 | 1.0 |
| dataset | upload | new | verified | 0.096 | 4.000 | 4115.3 | 4115.3 |
| dataset | upload | exact | verified | 0.046 | 4.000 | 6.9 | 6.9 |
| dataset | upload | edit | verified | 0.084 | 4.008 | 95.5 | 95.5 |
| dataset | upload | shift | verified | 0.078 | 4.016 | 103.6 | 103.6 |
| dataset | upload | tensor | verified | 0.087 | 4.000 | 778.3 | 778.3 |
| dataset | upload | tensor-edit | verified | 0.086 | 4.000 | 80.3 | 80.3 |
| dataset | upload | entropy | verified | 0.092 | 4.000 | 4244.5 | 4244.5 |
| dataset | upload | entropy-replace | verified | 0.094 | 4.000 | 4308.5 | 4308.5 |
| dataset | metadata | new | verified | 0.007 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | new | verified | 0.043 | 4.000 | 4107.9 | 4107.9 |
| dataset | download-cached | new | verified | 0.006 | 4.000 | 0.1 | 0.1 |
| dataset | download-forced | new | verified | 0.027 | 4.000 | 4106.4 | 4106.4 |
| dataset | metadata | exact | verified | 0.005 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | exact | verified | 0.064 | 4.000 | 4109.0 | 4109.0 |
| dataset | download-cached | exact | verified | 0.006 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | exact | verified | 0.064 | 4.000 | 4107.1 | 4107.1 |
| dataset | metadata | edit | verified | 0.020 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | edit | verified | 0.030 | 4.008 | 4205.1 | 4205.1 |
| dataset | download-cached | edit | verified | 0.006 | 4.008 | 0.0 | 0.0 |
| dataset | download-forced | edit | verified | 0.026 | 4.008 | 4185.1 | 4185.1 |
| dataset | metadata | shift | verified | 0.005 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | shift | verified | 0.021 | 4.016 | 4451.8 | 4451.8 |
| dataset | download-cached | shift | verified | 0.005 | 4.016 | 0.0 | 0.0 |
| dataset | download-forced | shift | verified | 0.019 | 4.016 | 4128.5 | 4128.5 |
| dataset | metadata | tensor | verified | 0.005 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | tensor | verified | 0.022 | 4.000 | 791.5 | 791.5 |
| dataset | download-cached | tensor | verified | 0.005 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | tensor | verified | 0.023 | 4.000 | 789.6 | 789.6 |
| dataset | metadata | tensor-edit | verified | 0.004 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | tensor-edit | error | 0.011 | 4.000 | 84.2 | 84.2 |
| dataset | download-cached | tensor-edit | error | 0.010 | 4.000 | 85.2 | 85.2 |
| dataset | download-forced | tensor-edit | error | 0.050 | 4.000 | 89.5 | 89.5 |
| dataset | metadata | entropy | verified | 0.005 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | entropy | verified | 0.025 | 4.000 | 4202.5 | 4202.5 |
| dataset | download-cached | entropy | verified | 0.005 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | entropy | verified | 0.019 | 4.000 | 4107.7 | 4107.7 |
| dataset | metadata | entropy-replace | verified | 0.004 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | entropy-replace | verified | 0.020 | 4.000 | 4108.1 | 4108.1 |
| dataset | download-cached | entropy-replace | verified | 0.005 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | entropy-replace | verified | 0.020 | 4.000 | 4107.7 | 4107.7 |

## OpenXet without the client chunk cache

Interface counters include protocol overhead and other traffic on the measured interface.
They do not report CAS allocation or exact per-request payload bytes.

| Resource | Operation | Case | Status | s | Logical MiB | TX KiB | RX KiB |
|---|---|---|---|---:|---:|---:|---:|
| dataset | create | — | verified | 0.019 | 0.000 | 1.0 | 1.0 |
| dataset | upload | new | verified | 0.149 | 4.000 | 4114.2 | 4114.2 |
| dataset | upload | exact | verified | 0.038 | 4.000 | 7.0 | 7.0 |
| dataset | upload | edit | verified | 0.100 | 4.008 | 95.5 | 95.5 |
| dataset | upload | shift | verified | 0.086 | 4.016 | 103.6 | 103.6 |
| dataset | upload | tensor | verified | 0.083 | 4.000 | 778.4 | 778.4 |
| dataset | upload | tensor-edit | verified | 0.089 | 4.000 | 80.3 | 80.3 |
| dataset | upload | entropy | verified | 0.101 | 4.000 | 4114.2 | 4114.2 |
| dataset | upload | entropy-replace | verified | 0.105 | 4.000 | 4115.0 | 4115.0 |
| dataset | metadata | new | verified | 0.008 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | new | verified | 0.049 | 4.000 | 4108.1 | 4108.1 |
| dataset | download-cached | new | verified | 0.005 | 4.000 | 0.1 | 0.1 |
| dataset | download-forced | new | verified | 0.030 | 4.000 | 4106.6 | 4106.6 |
| dataset | metadata | exact | verified | 0.005 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | exact | verified | 0.022 | 4.000 | 4108.4 | 4108.4 |
| dataset | download-cached | exact | verified | 0.005 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | exact | verified | 0.018 | 4.000 | 4107.4 | 4107.4 |
| dataset | metadata | edit | verified | 0.005 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | edit | verified | 0.024 | 4.008 | 4120.7 | 4120.7 |
| dataset | download-cached | edit | verified | 0.005 | 4.008 | 0.0 | 0.0 |
| dataset | download-forced | edit | verified | 0.023 | 4.008 | 4119.8 | 4119.8 |
| dataset | metadata | shift | verified | 0.005 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | shift | verified | 0.021 | 4.016 | 4128.6 | 4128.6 |
| dataset | download-cached | shift | verified | 0.005 | 4.016 | 0.0 | 0.0 |
| dataset | download-forced | shift | verified | 0.021 | 4.016 | 4126.8 | 4126.8 |
| dataset | metadata | tensor | verified | 0.005 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | tensor | verified | 0.022 | 4.000 | 791.6 | 791.6 |
| dataset | download-cached | tensor | verified | 0.005 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | tensor | verified | 0.023 | 4.000 | 789.6 | 789.6 |
| dataset | metadata | tensor-edit | verified | 0.005 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | tensor-edit | error | 0.011 | 4.000 | 84.3 | 84.3 |
| dataset | download-cached | tensor-edit | error | 0.008 | 4.000 | 80.3 | 80.3 |
| dataset | download-forced | tensor-edit | error | 0.008 | 4.000 | 82.1 | 82.1 |
| dataset | metadata | entropy | verified | 0.004 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | entropy | verified | 0.028 | 4.000 | 4115.1 | 4115.1 |
| dataset | download-cached | entropy | verified | 0.005 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | entropy | verified | 0.019 | 4.000 | 4107.1 | 4107.1 |
| dataset | metadata | entropy-replace | verified | 0.004 | 0.000 | 2.8 | 2.8 |
| dataset | download-fresh-hub-cache | entropy-replace | verified | 0.024 | 4.000 | 4108.6 | 4108.6 |
| dataset | download-cached | entropy-replace | verified | 0.005 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | entropy-replace | verified | 0.017 | 4.000 | 4108.3 | 4108.3 |

## Hugging Face hosted

Interface counters include protocol overhead and other traffic on the measured interface.
They do not report CAS allocation or exact per-request payload bytes.

| Resource | Operation | Case | Status | s | Logical MiB | TX KiB | RX KiB |
|---|---|---|---|---:|---:|---:|---:|
| bucket | create | — | verified | 0.630 | 0.000 | 2.4 | 3.8 |
| bucket | upload | new | verified | 7.044 | 4.000 | 123.0 | 2961.8 |
| bucket | download-new-target | new | verified | 7.360 | 4.000 | 45.3 | 4344.9 |
| bucket | download-same-target | new | verified | 6.533 | 4.000 | 59.3 | 4384.7 |
| bucket | upload | same-key | verified | 4.117 | 4.000 | 24.6 | 34.5 |
| bucket | download-new-target | same-key | verified | 7.795 | 4.000 | 53.7 | 4374.9 |
| bucket | download-same-target | same-key | verified | 7.457 | 4.000 | 58.2 | 4361.3 |
| bucket | upload | exact | verified | 1.575 | 4.000 | 16.6 | 25.2 |
| bucket | download-new-target | exact | verified | 4.760 | 4.000 | 41.5 | 4362.8 |
| bucket | download-same-target | exact | verified | 5.834 | 4.000 | 45.9 | 4344.7 |
| bucket | upload | edit | verified | 4.651 | 4.008 | 122.1 | 48.4 |
| bucket | download-new-target | edit | verified | 5.154 | 4.008 | 54.7 | 4387.7 |
| bucket | download-same-target | edit | verified | 6.327 | 4.008 | 57.5 | 4369.2 |
| bucket | upload | shift | verified | 5.320 | 4.016 | 139.1 | 81.3 |
| bucket | download-new-target | shift | verified | 5.743 | 4.016 | 42.2 | 4359.6 |
| bucket | download-same-target | shift | verified | 6.337 | 4.016 | 67.7 | 4391.0 |
| bucket | upload | tensor | verified | 6.183 | 4.000 | 799.3 | 65.2 |
| bucket | download-new-target | tensor | verified | 3.557 | 4.000 | 16.9 | 827.3 |
| bucket | download-same-target | tensor | verified | 4.903 | 4.000 | 38.6 | 876.3 |
| bucket | upload | tensor-edit | verified | 5.293 | 4.000 | 101.4 | 43.0 |
| bucket | download-new-target | tensor-edit | verified | 5.860 | 4.000 | 28.7 | 235.8 |
| bucket | download-same-target | tensor-edit | verified | 4.331 | 4.000 | 35.6 | 270.3 |
| bucket | upload | entropy | verified | 7.072 | 4.000 | 4144.2 | 58.2 |
| bucket | download-new-target | entropy | verified | 4.949 | 4.000 | 44.7 | 4364.7 |
| bucket | download-same-target | entropy | verified | 7.835 | 4.000 | 51.0 | 4345.2 |
| bucket | upload | entropy-replace | verified | 6.022 | 4.000 | 4149.9 | 89.2 |
| bucket | download-new-target | entropy-replace | verified | 6.979 | 4.000 | 36.7 | 4332.3 |
| bucket | download-same-target | entropy-replace | verified | 6.177 | 4.000 | 54.3 | 4356.6 |
| dataset | create | — | verified | 1.450 | 0.000 | 3.0 | 5.0 |
| dataset | upload | new | verified | 3.322 | 4.000 | 13.1 | 39.8 |
| dataset | upload | exact | verified | 3.267 | 4.000 | 15.7 | 22.3 |
| dataset | upload | edit | verified | 2.335 | 4.008 | 17.4 | 25.2 |
| dataset | upload | shift | verified | 2.934 | 4.016 | 22.8 | 31.1 |
| dataset | upload | tensor | verified | 2.512 | 4.000 | 21.9 | 31.0 |
| dataset | upload | tensor-edit | verified | 3.091 | 4.000 | 18.3 | 44.8 |
| dataset | upload | entropy | verified | 3.869 | 4.000 | 9.2 | 14.8 |
| dataset | upload | entropy-replace | verified | 4.167 | 4.000 | 24.1 | 32.9 |
| dataset | metadata | new | verified | 0.386 | 0.000 | 3.4 | 6.2 |
| dataset | download-fresh-hub-cache | new | verified | 5.770 | 4.000 | 51.6 | 4373.0 |
| dataset | download-cached | new | verified | 0.006 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | new | verified | 4.975 | 4.000 | 30.9 | 4323.0 |
| dataset | metadata | exact | verified | 0.370 | 0.000 | 0.5 | 3.0 |
| dataset | download-fresh-hub-cache | exact | verified | 5.242 | 4.000 | 54.4 | 4375.0 |
| dataset | download-cached | exact | verified | 0.006 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | exact | verified | 3.805 | 4.000 | 39.9 | 4334.0 |
| dataset | metadata | edit | verified | 0.513 | 0.000 | 2.4 | 4.9 |
| dataset | download-fresh-hub-cache | edit | verified | 5.299 | 4.008 | 43.6 | 4349.4 |
| dataset | download-cached | edit | verified | 0.006 | 4.008 | 0.0 | 0.0 |
| dataset | download-forced | edit | verified | 3.806 | 4.008 | 53.6 | 4383.8 |
| dataset | metadata | shift | verified | 0.437 | 0.000 | 3.4 | 6.7 |
| dataset | download-fresh-hub-cache | shift | verified | 4.962 | 4.016 | 56.5 | 4373.7 |
| dataset | download-cached | shift | verified | 0.005 | 4.016 | 0.0 | 0.0 |
| dataset | download-forced | shift | verified | 3.444 | 4.016 | 36.2 | 4349.0 |
| dataset | metadata | tensor | verified | 0.439 | 0.000 | 1.6 | 4.2 |
| dataset | download-fresh-hub-cache | tensor | verified | 3.292 | 4.000 | 26.7 | 843.6 |
| dataset | download-cached | tensor | verified | 0.006 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | tensor | verified | 2.374 | 4.000 | 14.2 | 804.9 |
| dataset | metadata | tensor-edit | verified | 0.386 | 0.000 | 1.9 | 4.2 |
| dataset | download-fresh-hub-cache | tensor-edit | verified | 2.957 | 4.000 | 28.8 | 235.9 |
| dataset | download-cached | tensor-edit | verified | 0.006 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | tensor-edit | verified | 2.405 | 4.000 | 16.4 | 220.8 |
| dataset | metadata | entropy | verified | 0.418 | 0.000 | 0.5 | 3.0 |
| dataset | download-fresh-hub-cache | entropy | verified | 5.060 | 4.000 | 40.3 | 4336.1 |
| dataset | download-cached | entropy | verified | 0.006 | 4.000 | 0.0 | 0.0 |
| dataset | download-forced | entropy | verified | 4.494 | 4.000 | 38.9 | 4351.8 |
| dataset | metadata | entropy-replace | verified | 0.389 | 0.000 | 0.9 | 3.5 |
| dataset | download-fresh-hub-cache | entropy-replace | verified | 5.706 | 4.000 | 45.0 | 4339.6 |
| dataset | download-cached | entropy-replace | verified | 0.006 | 4.000 | 0.5 | 0.1 |
| dataset | download-forced | entropy-replace | verified | 3.813 | 4.000 | 43.2 | 4358.3 |
