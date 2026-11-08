#!/usr/bin/env python3
"""B8 bounded probe for the Parquet-Brotli decision: for every Brotli-compressed page of the given Parquet files,
re-encode the decoded page with libbrotli (python-brotli) over (quality, lgwin, mode) and report the exact-reproduction rate.
Usage: parquet_brotli_parity.py FILE.parquet..."""
import sys, struct, itertools, collections
import pyarrow.parquet as pq, brotli

def uvar(b, i):
    r = s = 0
    while True:
        c = b[i]; i += 1; r |= (c & 0x7f) << s; s += 7
        if not c & 0x80: return r, i
def zz(n): return (n >> 1) ^ -(n & 1)
def skip(b, i, t):
    if t in (1, 2): return i
    if t == 3: return i + 1
    if t in (4, 5, 6): return uvar(b, i)[1]
    if t == 7: return i + 8
    if t == 8: n, i = uvar(b, i); return i + n
    if t in (9, 10):
        h = b[i]; i += 1; n = h >> 4; et = h & 15
        if n == 15: n, i = uvar(b, i)
        for _ in range(n): i = skip(b, i, et)
        return i
    if t == 12: return struct_(b, i)[1]
    raise ValueError(t)
def struct_(b, i):
    fid = 0; vals = {}
    while True:
        h = b[i]; i += 1
        if h == 0: return vals, i
        d, t = h >> 4, h & 15
        if d == 0: v, i = uvar(b, i); fid = zz(v)
        else: fid += d
        if t in (1, 2): vals[fid] = t == 1
        elif t in (5, 6): v, i = uvar(b, i); vals[fid] = zz(v)
        elif t == 12: vals[fid], i = struct_(b, i)
        else: i = skip(b, i, t)

CONFIGS = [(q, 22, 0) for q in (5, 6, 7, 8, 9)] + [(8, 18, 0), (8, 24, 0), (11, 22, 0)]
MAXP = int(__import__('os').environ.get('MAXP', '30'))  # pages per file (q11 is slow)
tot = collections.Counter(); hit = collections.Counter(); anyhit = 0; pages = 0; unrep_bytes = 0; all_bytes = 0
for path in sys.argv[1:]:
    f = pq.ParquetFile(path); raw = open(path, "rb").read()
    md = f.metadata; fp = 0
    for rg in range(md.num_row_groups):
        for c in range(md.num_columns):
            cc = md.row_group(rg).column(c)
            if str(cc.compression) != "BROTLI": continue
            start = cc.dictionary_page_offset if cc.has_dictionary_page and cc.dictionary_page_offset else cc.data_page_offset
            end = start + cc.total_compressed_size; i = start
            while i < end:
                hdr, j = struct_(raw, i)
                ucs, cs = hdr[2], hdr[3]
                payload = raw[j:j + cs]; i = j + cs
                dec = brotli.decompress(payload)
                assert len(dec) == ucs
                if fp >= MAXP: continue
                fp += 1; pages += 1; all_bytes += cs; ok_any = False
                for cfg in CONFIGS:
                    if cfg[0] == 11 and len(dec) > 400000: continue
                    tot[cfg] += 1
                    if brotli.compress(dec, quality=cfg[0], lgwin=cfg[1], mode=cfg[2]) == payload: hit[cfg] += 1; ok_any = True
                anyhit += ok_any
                if not ok_any: unrep_bytes += cs
print(f"pages {pages}, compressed bytes {all_bytes}")
for cfg in CONFIGS:
    if hit[cfg]: print(f"quality={cfg[0]} lgwin={cfg[1]} mode={cfg[2]}: {hit[cfg]}/{tot[cfg]} pages exact")
print(f"pages reproduced by at least one config: {anyhit}/{pages}; unreproduced compressed bytes {unrep_bytes}")
