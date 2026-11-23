#!/usr/bin/env python3
"""Regenerate the 'Source map' (zeros3.go) or 'Test map' (zeros3_test.go) line numbers from the section headers.
usage: srcmap.py zeros3.go | zeros3_test.go
check: from testing-harnesses: go run ./harness/toc_check -file ../zeros3.go ; go run ./harness/toc_check -file ../zeros3_test.go"""
import re, sys
p = sys.argv[1]
L = open(p).read().split("\n")
hdr = {}
for i in range(1, len(L)):
    if re.match(r"^// =+$", L[i-1]) and re.match(r"^// \S", L[i]) and not re.match(r"^// =+$", L[i]):
        hdr.setdefault(L[i], i + 1)
def find(prefix):
    for t, n in hdr.items():
        if t.startswith(prefix):
            return n
    raise SystemExit("no section header starts with %r" % prefix)
# (map description, header prefix); line 1 is the package overview
ents = [
 ("Package overview, imports, constants, and shared utilities", None),
 ("Content-defined chunking (CDC)", "// 3. Content-defined chunking"),
 ("Content-addressed chunk storage (CAS)", "// 4. Content-addressed chunk storage"),
 ("Packed CAS (immutable packs, DEFLATE and restartable records, locator index, coalesced reads)", "// 4b. Packed CAS"),
 ("Small-object segments (inline manifests, store level 11)", "// 4d. Small-object segments"),
 ("Manifests (immutable, JSON)", "// 5. Manifests"),
 ("Visibility journal (append-only, checksummed)", "// 6. Visibility journal"),
 ("Store: format, namespace, and object CRUD", "// 7. Store: format metadata"),
 ("Compact namespace generations: paged tables, sorted overlays, consolidation (B3)", "// 7a. Compact namespace"),
 ("Version history/restore, history pruning, ListObjectsV2", "// 7c. Internal object version history"),
 ("SigV4 authentication (header and presigned-URL)", "// 8. AWS SigV4"),
 ("Request payload checksums and S3-shaped XML error/response types", "// 9. Request payload checksums"),
 ("HTTP routing and S3 operation handlers", "// 10. Raw HTTP routing"),
 ("Conditional operations (PUT/GET/HEAD preconditions)", "// 10a. S3 conditional-write"),
 ("CopyObject", "// 11. CopyObject"),
 ("Multipart upload", "// 11b. Multipart upload"),
 ("Stats and reachability scanning", "// 12. Stats and reachability"),
 ("Verify", "// 13. Verify"),
 ("Store locking and safe offline GC", "// 13b. Store locking"),
 ("Offline compaction (`zeros3 compact`)", "// 13c. Offline compaction"),
 ("Pack reclamation and repacking (`zeros3 repack`)", "// 13d. Pack reclamation"),
 ("Reconstruction-ordered pack layout: co-access probes, anchors, estimator (B5)", "// 13d1. Reconstruction-ordered"),
 ("Online maintenance: epochs, pins, live GC and repack (B4)", "// 13d2. Online maintenance"),
 ("Physical tiers: status and pack movement (`zeros3 tier`)", "// 13e. Physical tiers"),
 ("Content-aware tier policy and rebalance (`zeros3 tier policy|rebalance`)", "// 13f. Content-aware tier policy"),
 ("Streaming object reads (full and ranged GET), verified chunk cache, pipelining", "// 14. Streaming object reads"),
 ("Delta sync client, credentials, and parallel transfer", "// 15. Optional ZeroS3 Delta Sync"),
 ("Bulk logical-chunk transport (v2)", "// 15b-ter. Bulk logical-chunk"),
 ("Recursive directory sync", "// 15c. `zeros3 sync` directory"),
 ("Remote replication (`zeros3 replicate`)", "// 15d. `zeros3 replicate` client"),
 ("Peer-assisted corruption repair (`zeros3 repair`)", "// 15e. Peer-assisted corruption repair"),
 ("Namespace (prefix/bucket) replication", "// 15f. Namespace"),
 ("Copy-on-write namespace fork (`zeros3 fork`)", "// 15g. Copy-on-write namespace fork"),
 ("Snapshots and restore", "// 15h. Durable namespace snapshots"),
 ("Structural diff and inspect (introspection)", "// 15j. Structural object diff"),
 ("Portable snapshot bundles (`zeros3 bundle`)", "// 16b. Portable snapshot bundles"),
 ("Delta (thin) snapshot bundles (`zeros3 bundle export -base-snapshot`)", "// 16c. Delta (thin) snapshot bundles"),
 ("Structured representations: optional tar/SQLite/CAR/Merkle storage (Z3-I0)", "// 16d. Structured representations"),
 ("Representation-authoritative bodies: pin/representation/delta, closure, revert (Z4-C1)", "// 16e. Representation-authoritative bodies"),
 ("Global representation-graph optimization: closure cost, search, transitions (B6)", "// 16e1. Global representation-graph"),
 ("Region-aware similarity discovery: positional fingerprints, posting index, complementary bases (B7)", "// 16e2. Region-aware similarity discovery"),
 ("Cross-format canonical streams: body references, ZIP replay, canonical graph nodes (B8)", "// 16e3. Cross-format canonical"),
 ("Windowed and estimated similarity deltas (extracted dcore)", "// 16f. Windowed and estimated"),
 ("ZN7 model container and ggml quantizers (extracted numcore/qcore) and ZN7 authoritative bodies", "// 16g. ZN7 windowed model container"),
 ("Exact Brotli and LZ4 replay (extracted brotliz3/lz4z3): Parquet pages and SST blocks of the hard codecs", "// 16i. Exact Brotli and LZ4 replay"),
 ("CLI dispatch, HTTP server/startup, and main", "// 17. Lifecycle / main"),
]
TESTS = [  # (header prefix) in the order of the Test map rows
 "// Shared test helpers", "// Store / format tests", "// SigV4 tests", "// CRC32 tests", "// End-to-end HTTP tests",
 "// M2: Bucket tests", "// M3: CDC/dedup evidence", "// M3: CopyObject", "// M3: single-range GET", "// M5-B: Multipart upload tests",
 "// Presigned URL (SigV4 query-string", "// M5-C: internal object version history", "// History retention (prune)", "// M5-D: ListParts",
 "// M6 -- Optional ZeroS3 Delta Sync", "// M6C -- recursive directory sync", "// M8A: remote-to-remote", "// M8B: peer-assisted corruption repair",
 "// M8C: prefix/bucket namespace replication", "// M8D: copy-on-write namespace fork", "// M8E-A: durable namespace snapshots",
 "// M8F-A: atomic conditional PutObject", "// M8G-A:", "// M8H-B1/B2", "// P1-A: Environment-variable credentials",
 "// Streaming ingest: ordinary PutObject", "// Streaming reads and aws-chunked SigV4", "// Z2-03: packed CAS", "// Z2-04: pack-aware gc",
 "// Z2-05: adaptive pack compression", "// Z2-06: scalable packed-chunk locator", "// Z2-07: bulk logical-chunk transport",
 "// Hot/warm/cold physical pack tiers", "// Z2-09: GetBucketLocation", "// Content-aware tier policy and rebalance",
 "// Z2-11: grouped loose-CAS publication", "// Portable snapshot bundles (section 16b)", "// Z2-13: locality-aware pack layout",
 "// Z2-14: delta (thin) snapshot bundles", "// Z2-15: stream-native direct pack ingest",
]
if p.endswith("_test.go"):
    start = next(i for i, l in enumerate(L) if l == "// Test map")
    rows = [i for i in range(start, start + 120) if re.match(r"^//\s+\d+\s+\S", L[i])]
    assert len(rows) == len(TESTS), (len(rows), len(TESTS))
    for r, pre in zip(rows, TESTS):
        d = re.match(r"^//\s+\d+\s+(\S.*)$", L[r]).group(1)
        L[r] = "//   %6d    %s" % (find(pre), d)
    open(p, "w").write("\n".join(L))
    sys.exit(0)
start = next(i for i, l in enumerate(L) if l == "// Source map")
rows = [i for i in range(start, start + 120) if re.match(r"^//\s+\d+\s+\S", L[i])]
first, last = rows[0], rows[-1]
out = []
for d, h in ents:
    n = 1 if h is None else find(h)
    out.append("//   %6d    %s" % (n, d))
# the table header lines sit above the first row; replace rows only
assert last - first + 1 == len([i for i in range(first, last + 1)]), "rows are contiguous"
L[first:last + 1] = out
open(p, "w").write("\n".join(L))
