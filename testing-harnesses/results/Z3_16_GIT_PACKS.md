# Z3-16 — Git packs and loose objects (decision record)

Laboratory result; no production file changed (`zeros3.go`, root `go.mod`, manifests, journal, pack/store formats, read paths, GC/roots,
descriptors, native-ID indexes). Code: `testing-harnesses/z3` (`gitz3/`, `cmd/z3git/`, `oracle/gitdelta/`, `realworld/git.json`); the Z3 module
is stdlib-only. `git` 2.43.0 / zlib 1.3 is an optional **producer** and Git's `create_delta` an optional **oracle**; neither is needed to
parse, reconstruct or persist. Nothing here is permission to productionize.

**Question.** Git packs churn (order, zlib bytes, delta bases) while the object set barely changes. How much churn can ZeroS3 remove while
reproducing the exact repository bytes, and where does it stop paying?

**Conclusion.** Ordinary CDC of packs loses almost all reuse (a single new commit costs 53–77 % of the pack on real repositories) even though
89–99 % of entry bytes are identical. Treating pack *entries* as components and deriving headers, OFS distances, Adler-32, trailer and `.idx`
removes most of it; exact DEFLATE reconstruction removes the rest after a recompression. The cost is a first-ingest premium and slow replay,
so admission must be per-class with a break-even probability, not a threshold.

## Exactness (all rows)

Every non-oracle row serialises its piece list (table layout, bytes must equal the accounted size), deserialises it and rebuilds the file from
stored components only; length, SHA-256 and bytes are compared (a non-oracle plan with no reconstruction path now fails the row). 0 inexact
rows. Every entry of every pack reconstructed exactly for this producer (levels 1/6/9, 12–13 MB blobs included); `.idx` v2 is byte-derivable
from the pack in every state. Derived and verified per instance: header/count, trailer, type/size, OFS distance, zlib header, Adler-32,
DEFLATE stream (IR), loose path. Not derivable: pack order (⌈log₂ n⌉ bits/entry), delta base choice. Unparsable, thin, REF-cyclic or
limit-exceeding input is carried as ordinary CDC bytes.

## Result 1 — cost of B given A, % of pack size (synthetic 0.87 MiB, SHA-1; SHA-256 within 1.5 points)

| A → B | CDC | gp-entry | gp-zlib | gp-set | gp-set-plain | new objects only |
|---|---:|---:|---:|---:|---:|---:|
| reversed object order | **30.8** | 4.7 | 2.8 | 4.0 | 4.6 | 0 |
| OFS_DELTA → REF_DELTA | 14.7 | 4.1 | 2.0 | 3.1 | 3.0 | 0 |
| window 3 / depth 3 | 4.3 | 4.1 | 2.4 | 3.0 | 3.5 | 0 |
| compression level 6 → 1 | **99.9** | 101.4 | 100.0 | 14.5 | **1.5** | 0 |
| loose objects → first pack | **99.6** | 101.5 | 100.0 | 7.6 | 5.8 | 0 |
| one small revision, full `-f` repack | 19.0 | 3.3 | 1.9 | 1.2 | 1.5 | 0.2 |

Real histories (shallow windows, packs produced locally by one git), one revision later, % of pack:

| repository | CDC | gp-zlib | gp-set | gp-set-plain | new objects only |
|---|---:|---:|---:|---:|---:|
| lua (8 431 objects) | **77** | 15 | 6.2 | 6.6 | 1.2 |
| imgui (2 616) | 53 | 13 | 5.3 | 5.7 | 11 |
| codicons (7 153) | 58 | 7.6 | 2.6 | 2.8 | 1.8 |
| zeros3 (1 243) | 54 | 7.9 | 9.8 | 10.3 | 6.7 |

Churn is four mechanisms, each removed by different knowledge: **order** (entry boundaries), **dispersed header edits** (OFS distances: lua
one revision changes 0.3 % of entry bytes but 9.5 % are OFS shifts spread over the pack; `OFS → REF` costs CDC 75 %), **zlib bytes**
(level change: 100 % of B's bytes differ; only exact DEFLATE recovers it — `gp-set-plain` 1.5–11 % vs CDC 93–100 %; 51.6 MB APK pack 0.12 %),
**delta base choice** (kept in 91–99.9 % of deltas across revisions, 29–64 % under window/depth or reorder changes; whenever a base is kept the
program is kept). Packs rewritten by `gc` after a few revisions cost the exact layouts 1.9× less cumulative storage than CDC.

## Result 2 — where it does not pay (not hidden)

* First-ingest premium over CDC of pack + `.idx`: gp-zlib −0.1…+1.1 %, gp-auto +2…6 %, gp-set +3.5…18 % (lua), gp-set-plain +5…21 %. Break-even probability `q*` of a rewrite event: gp-zlib 0.1–4 %, gp-set 15–25 %, gp-set-plain 5–26 % (more tiny entries → higher). Five APKs: −10 % (plaintext dedupe and recompression already win).
* Appended second pack (0.2 % everywhere), sparse edits of a 1 MiB blob (every 64 KiB chunk touched: CDC 878 K, exact 661–689 K), window/depth rewrites of most deltas (lua w3d3: CDC 85 %, gp-zlib 73 %, gp-set 84 %), and small packs (zeros3: gp-zlib 7.9 % beats gp-set 9.8 %).
* `set` beats `seq` only above ≈ 5 000 entries (2.4–2.9×); below ≈ 1 500 `seq` is cheaper. The native-object "floor" is not a floor for packs (lua: 45 M as full objects vs a 2.3 MB pack).
* Exact-DEFLATE replay runs at 1.1–4.4 MiB/s and analysis at 0.2–0.8 MiB/s, far below memory-speed concatenation; gp-zlib avoids replay. Parse/resolve are linear (≈ 35 MiB/s large objects, ≈ 13 µs/entry tiny). Peak heap of the lab replay is ≈ 11× the pack.

## Result 3 — reads, identity, delta oracle

* Reading one object by native id: `set` layout 4.9 K vs CDC 155 K (idx 23 K + chunks) on a 824-entry pack: 16–51× less, 1.7–4.6× the object's own packed bytes; content reads never need DEFLATE replay (serving original pack bytes does).
* SHA-1 needs a 52 B/object native→component map (2.3 % of the pack); SHA-256 removes the table for non-delta single-component objects (60 % of entries), but ids are 1.6× wider (+21 % table cost); whole-representation cost differs ≤ 1.2 points between formats.
* Git's `create_delta` regenerates **100 %** of stored delta programs (524/524 synthetic, lua 6 485, imgui 1 963, codicons 3 362, zeros3 559; `diff-delta.c` v1.7.12…v2.50.0 identical). Not realisable: it needs a GPL-source reimplementation and only saves the *new* programs of an event (0.8–19.8 KB, ≤ 2.3 % of a pack). Producer reproducibility: same command twice → identical pack and idx in 14/14 configurations.

## Admission

No static size threshold. `gitz3.Admit(pack, idx, algo, q)` picks by expected cost and answers CDC on any parse/resolve/checksum/limit/thin
failure or when exact ≥ CDC. gp-zlib is the safe default (≈ free; wins reorder/rebase/repack-after-commit); plaintext tier only where
recompression is likely (loose → pack is certain: always admit plaintext-first); `set` layout for ≥ 5 000-entry packs or id lookups.

## Production-portability status

* Reusable largely unchanged: pack/idx/delta/loose parsers and bounds; `ParsePackAt(io.ReaderAt, size)` (bounded reads, checked offsets, no allocation from declared lengths, streaming `InflateTo`, `Range`/`WriteRange`; no whole-pack copy retained); recipe (de)serialisation.
* Needs Store/CAS wiring: a `ReaderAt` over CAS-backed pack bytes; component fetch behind `get(ID)`; native-ID → component index (SHA-1); replacing whole-payload `Pack.Inflate` in delta resolution and component builders with bounded CAS-backed storage (remaining debt).
* Deliberately lab-only: producer harness, scenarios, `Admit` constants, accounting/reports, `[]byte` plan pieces, in-memory replay store.
* Still needs redesign: exact-DEFLATE replay speed and plan analysis cost; the 5–21 % plaintext premium; delta derivation (GPL oracle).

## Limitations

One producer (git 2.43.0, zlib 1.3, threads = 1); no zlib-ng/libdeflate/libgit2/JGit packs. SHA-256 only on synthetic families. No bitmaps,
commit-graph, MIDX, reverse indexes, promisor/cruft packs or alternates. Real histories are 150–1 500-commit shallow windows of four
repositories (SHAs in `realworld/git.json`); binary coverage is one synthetic APK re-export.

## Reproduce and validate

```
cd testing-harnesses/z3 && export Z3_WORK=$TMPDIR/z3git
go run ./cmd/z3git probe | matrix -algo sha1 | real -repos lua,imgui,codicons,zeros3 | admit | reads | scale -sweep all | derive -pairs
gofmt -l . && go vet ./... && go test ./...
```

Validation: the deterministic fixture, hostile-pack, truncation-at-every-byte and mutation suites pass; ReaderAt and byte-backed parsing are
asserted structurally identical (entries, ids, objects, byte-identical `.idx`); short, hostile-size and truncated sources fail safely; a
32 MiB pack parses with allocation under a quarter of its size. After the refactor and the stricter exactness check, the synthetic
`z3git matrix` reorder / level / small-rev scenarios were re-run and reproduce the rows above (e.g. reversed CDC 30.8 %, gp-zlib 2.8 %);
the real-repository campaign was not re-run (parsing and accounting are unchanged and no new fetch was needed).
