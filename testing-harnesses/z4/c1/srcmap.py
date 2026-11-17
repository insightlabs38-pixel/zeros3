#!/usr/bin/env python3
"""Regenerate the 'Source map' line numbers of zeros3.go from its section headers.
usage: srcmap.py zeros3.go   (then: go run ./harness/toc_check -file ../zeros3.go from testing-harnesses)"""
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
 ("Packed CAS (immutable packs, DEFLATE records, locator index, coalesced reads)", "// 4b. Packed CAS"),
 ("Small-object segments (inline manifests, store level 11)", "// 4d. Small-object segments"),
 ("Manifests (immutable, JSON)", "// 5. Manifests"),
 ("Visibility journal (append-only, checksummed)", "// 6. Visibility journal"),
 ("Store: format, namespace, and object CRUD", "// 7. Store: format metadata"),
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
 ("Windowed and estimated similarity deltas (extracted dcore)", "// 16f. Windowed and estimated"),
 ("CLI dispatch, HTTP server/startup, and main", "// 17. Lifecycle / main"),
]
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
