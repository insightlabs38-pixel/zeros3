# Z3-11: OCI / tar structure-aware representation

Laboratory evidence only. `zeros3.go`, the root `go.mod`, the store, manifests, journal and every persistent format are untouched; no
production transform descriptor, read path, GC root or semantic CAS was added. Everything lives under `testing-harnesses/z3`.

The question: after Z3-08 normalised the DEFLATE layer (compression-induced churn is "solved" on container layers), is the remaining
plaintext floor really about the files, or is it tar metadata hiding unchanged file content? And can the same reversible-representation
architecture expose stable member content beneath `gzip`?

```sh
cd testing-harnesses/z3
go run ./cmd/z3fetch -group debian-oci-layer,alpine-oci-layer,go-src,grep-gnu,coreutils-gnu,typescript-npm,tokio-crate,syn-crate,lodash-npm,django-sdist,six-sdist -out "$TMPDIR/tgz" -max-mib 600
go run ./cmd/z3fetch -group python-oci-deps,python-oci-runtime,python-oci-tail -out "$TMPDIR/oci" -max-mib 200
go run ./cmd/z3corpus gen -out "$TMPDIR/tar" -size-kib 1024 -revisions 3 -families 'tar-*,targz-*'
R=cdc,oracle-logical,irp-tok16k,tarmem,tarmem-s,tarmem-cdc64k,oracle-tarmem,oracle-tarmem-s,oracle-tarmem-cdc64k,irp-tok16k+tarmem,irp-tok16k+tarmem-s,irp-tok16k+tarmem-cdc64k
go run ./cmd/z3ir family -corpus "$TMPDIR/tgz" -reps $R -json tgz.json       # also oci, tar
go run ./cmd/z3tar parse|churn|range -corpus DIR
go run ./cmd/z3tar admit -corpus DIR -from tgz.json
go run ./cmd/z3tar cost -from tgz.json,oci.json
go test ./tarz3 ./lab -run 'Tar'
```

## What was built

* `tarz3/` - exact tar structure parser (stdlib only, ~450 lines). It walks 512-byte blocks and never regenerates a header. `Parse` returns the
  member layout (header blocks, PAX `x`/`g` and GNU `L`/`K` prefix entries with their data, payload, block padding, trailer);
  `Split` separates the **structural stream** (headers, prefix entries, trailer: every byte that is not file content and not implied zero padding)
  from the **payloads**; `Join` rebuilds the tar from structure + payloads alone, walking the headers inside the structure stream. Handles V7,
  POSIX ustar, GNU (old `ustar  ` magic), PAX extended headers (incl. a `size` override) and base-256 sizes; header-only types
  (`1`-`6`) carry no payload as in `archive/tar`. Anything it cannot account for exactly (bad checksum, size/padding that does not fit, non-zero
  padding, GNU sparse `S`, truncated archive, oversized metadata entry) returns `*UnsupportedError` and callers use plain bytes.
  Tests: all of USTAR/PAX/GNU with long names, unicode, xattr PAX records, hardlink/symlink/dir/empty files, 10 KiB record padding,
  agreement with `archive/tar` on name/size/type/contents, truncation at ~every offset, 4 000 single-bit corruptions (reject, or accept *and* rebuild
  exactly), checksum/size/sparse/padding rejects, base-256 size, `Join` refusing a payload of the wrong size.
* `lab/tarreps.go` - plaintext segmentation `tarmem*` (below) usable three ways with no new accounting machinery: on a plain tar (exact, `Concat`), on
  a gzip's inflated plaintext (`oracle-tarmem*`: does not reconstruct the gzip), and as the **plaintext cutter of the Z3-08 predictive DEFLATE
  representation** (`irp-tok16k+tarmem*`, `IRSpec.TarPlain`): the tar pieces *are* the plaintext components the DEFLATE units refer to, so there is one
  representation graph and no second copy of the decompressed tar. A plaintext that is not a parseable tar falls back to CDC and is still exact.
* `corpus/gen_tar.go` - 17 controlled families (below). `cmd/z3tar` - `parse`, `churn`, `range`, `admit`, `cost`.
* `realworld/sources.json` - three new pinned layer families, `python:3.12.{1..5}-slim-bookworm` layers #1 (apt dependencies), #2 (CPython), #4
  (pip/setuptools/wheel): 15 blobs, 70 MiB, registry digest == SHA-256 (layer #0 is the Debian base layer already pinned).

Segmentation (`lab.TarSpecs`): per member one *header piece* (its header block, plus PAX/GNU prefix entries) and one *payload piece* (payload and its
zero padding, identified by content only: same bytes, same component, whatever the path, mtime, mode, owner, neighbours or order). `tarmem-s`
merges payloads <= 512 B into the header piece; `tarmem-cdc64k` additionally cuts payloads >= 64 KiB with the production CDC. Whole-member identity
and CDC inside a member are the same representation with a threshold, not two subsystems.

## Exactness

* Plain tar: every `tarmem*` row on every synthetic and real family rebuilds the tar byte for byte (`exact` x/x).
* **DEFLATE + tar composition**: `irp-tok16k+tarmem*` rebuilds the *original gzip bytes* (length, SHA-256, bytes) from tar components
  (header pieces + payload pieces) + the DEFLATE skeleton and units on all 14 real families (every row `exact` x/x, e.g. 4/4 Debian) and the 3 synthetic `targz-*`
  families. The reconstruction path is exactly: payload/structure components -> exact tar bytes (concatenation) -> DEFLATE replay -> gzip bytes.
* All 63 real archives parse (V7 grep/coreutils, GNU tokio/syn/six-1.12, ustar OCI/Go/npm, PAX prefixes on django (9 981 entries), six >= 1.13, Go (2), python layers (3)); none was rejected. Fail-safe is
  tested (`lab.TestTarRepsExactAndSemantic`: a gzip that is not a tar still reconstructs exactly through plain CDC components; unit tests: damaged/sparse/odd tars are rejected, never mis-split).

## Controlled mutations (`z3corpus gen -families 'tar-*,targz-*'`, 1 MiB trees, 3 revisions)

Update-revision cost, % of the stored bytes of the update revisions (packed estimate, 96 B per component reference); the `CDC` column is what plaintext CDC pays on the same tar.

| mutation | plaintext CDC | `tarmem` | `tarmem-cdc64k` | what is unchanged by ground truth |
|---|---:|---:|---:|---|
| global mtime shift (ustar / GNU) | 25.4 / 25.7 | **1.8 / 1.6** | 1.9 / 1.7 | every payload |
| uid/gid of every member | 29.2 | **1.5** | 1.5 | every payload |
| mode of 10% of files | 4.0 | 1.4 | 1.4 | every payload |
| PAX records of every member | 25.3 | **2.0** | 2.1 | every payload |
| member order permuted | 30.9 | **1.1** | 1.1 | every payload |
| one file renamed / one directory renamed | 2.7 / 8.6 | **0.9 / 1.2** | 0.9 / 1.3 | payload of the moved files (path is not part of payload identity) |
| symlink retarget + hardlinks | 14.0 | 1.3 | 1.3 | every payload |
| one file added / removed | 5.1 / 4.8 | 1.7 / 1.0 | 1.8 / 1.0 | all other payloads |
| one file rewritten | 4.1 | 1.9 | 2.0 | all other payloads |
| **200 B inserted into the largest member** | 10.3 | 9.1 | **5.4** | the member's own chunks away from the edit (needs CDC *inside* the member) |
| release-like (global mtime shift + 1 file modified + 1 added) | 31.9 | **3.1** | 3.2 | all but two payloads |
| `targz-*` (gzip level 6) mtime shift / modify one / release | 100.2 / 64.4 / 95.3 | 7.0 / 11.6 / 8.9 (`oracle-tarmem`) | 7.3 / 11.9 / 9.1 | as above |
| same, DEFLATE-only `irp-tok16k` | 83.5 / 22.8 / 101.4 | | | |
| same, **DEFLATE + tar** `irp-tok16k+tarmem` | **12.2 / 14.7 / 14.8** | | | exact gzip bytes |

The residual 1-2% on metadata-only changes is almost entirely the reference cost (`refs` 1.0-1.4%), not header bytes. Rename behaviour is explicit: path is part of the
member's *header*, never of its payload component, so a renamed file costs one new 512-byte header and no payload.

## Real corpus, compressed layers (`z3ir family`, update revisions, % of the compressed bytes)

`CDC` = raw compressed CDC (ZeroS3 today). `DEFLATE-only` = `irp-tok16k` (Z3-08). `plaintext floor` = `oracle-logical` (plaintext CDC, no
reconstruction claim). `tar only` = `oracle-tarmem*` best (on gzip inputs an oracle) (it ignores the cost of rebuilding the gzip bytes). `DEFLATE + tar` = best
`irp-tok16k+tarmem*` variant, exact.

| family | members | CDC | DEFLATE-only | plaintext floor | tar only (oracle) | **DEFLATE + tar** (variant) | tar saves vs DEFLATE-only |
|---|---:|---:|---:|---:|---:|---:|---|
| **Debian bookworm-slim layer** (4 builds) | 4 221 | 100.0 | 18.6 | 16.8 | 7.5 | **9.3** (cdc64k) | 2.0x |
| **Go source tarball** (1.21.0-1.21.3) | 13 893 | 99.5 | 79.3 | 75.5 | 15.9 | **19.7** (plain) | 4.0x |
| **python:3.12-slim layer #2 (CPython)** | 1 344-1 398 | 100.1 | 73.6 | 70.9 | 40.1 | **42.8** (cdc64k) | 1.7x |
| python:3.12-slim layer #4 (pip tail) | 522-1 341 | 100.1 | 99.5 | 95.8 | 48.4 | **52.0** (cdc64k) | 1.9x |
| python:3.12-slim layer #1 (apt deps) | 535-552 | 100.1 | 54.5 | 51.9 | 44.0 | **46.5** (cdc64k) | 1.2x |
| django sdist (5.0.1-5.0.4) | 9 992 | 100.1 | 104.2 | 99.4 | 39.9 | **44.6** (cdc64k) | 2.3x |
| syn crate | 98 | 100.1 | 111.0 | 105.3 | 40.9 | **46.6** (plain) | 2.4x |
| tokio crate | 510 | 100.1 | 116.1 | 110.0 | 62.2 | **68.3** (plain) | 1.7x |
| TypeScript npm tarball | 116 | 100.1 | 43.2 | 40.1 | 38.3 | **41.4** (cdc64k) | 1.04x |
| Alpine 3.19-3.20 rootfs layer | 517 | 100.1 | 84.0 | 81.2 | 76.1 | 78.9 (cdc64k) | 1.06x |
| six sdist | 19 | 100.4 | 108.6 | 103.7 | 94.7 | 99.6 | (tiny: 34 KiB) |
| lodash npm tarball | 1 049 | 100.2 | **87.3** | 84.3 | 94.4 | 97.5 | **loses** (refs 48-64%) |
| GNU grep tarballs | 1 235-1 292 | 97.7 | 107.4 | 100.0 | 99.6 | 107.1 | **loses** (every member changed) |
| GNU coreutils tarballs | 3 038-3 175 | 100.1 | 108.0 | 100.3 | 113.1 | 120.7 | **loses** (every member changed) |

Three plain statements. (1) On real container layers the answer is yes: Debian goes 99.9 -> 18.6 (Z3-08) -> **9.3** and the Go source tarball, which Z3-08 could not
beat (79%), goes to **19.7%**. (2) The gain is a property of *member reuse*, not of tar: where every member changed (grep, coreutils, Alpine's rootfs of fresh
binaries) tar semantics cost more than they save. (3) Composing with DEFLATE costs 1.8-6.1 points over the tar-only oracle (unit blobs 0.3-4.7%, skeleton 0.5-0.8%, extra references
for the units) - the price of exactly rebuilding the gzip bytes, matching Z3-08.

## Q1-Q10

**1. How much of the remaining plaintext floor is tar metadata churn?** `z3tar churn` attributes every plaintext-CDC-new byte to the tar region it lies in (update pairs, byte-weighted):

| family | plaintext CDC new % | in structure % | in payload of members that exist in the base % | in payload of new members % | member-level new payload % | metadata + boundary churn / floor |
|---|---:|---:|---:|---:|---:|---:|
| Debian layer | 22.8 | 3.2 | **14.9** | 4.7 | 4.7 | **79%** |
| Go source | 73.4 | 8.2 | **64.1** | 1.1 | 1.3 | **98%** |
| django sdist | 95.3 | 28.0 | **61.7** | 5.6 | 5.8 | 94% |
| syn crate | 94.5 | 3.7 | 66.7 | 24.1 | 25.2 | 75% |
| tokio crate | 99.5 | 9.0 | 58.6 | 31.9 | 32.1 | 68% |
| python layer #2 (CPython) | 78.7 | 2.8 | 38.7 | 37.2 | 39.3 | 53% |
| python layer #4 (tail) | 92.7 | 6.0 | 50.6 | 36.1 | 36.0 | 61% |
| python layer #1 (deps) | 58.4 | 2.8 | 10.2 | 45.4 | 48.7 | 22% |
| Alpine layer | 83.5 | 3.7 | 7.7 | 72.2 | 77.5 | 14% |
| grep tarballs | 95.3 | 6.9 | 20.3 | 68.1 | 71.9 | 29% |
| coreutils tarballs | 98.8 | 4.3 | 1.4 | 93.1 | 94.1 | 6% |
| TypeScript npm | 36.9 | 0.1 | 0.6 | 36.2 | 85.8 | 2% |

"Metadata + boundary churn" = structure plus payload bytes of members whose content *exists in the base* (CDC re-cut them only because a header in front changed
and shifted/changed the chunk). On Debian 79% of the 22.8% floor, on Go source 98%, is not new file content. The synthetic controls give the same shape (mtime shift: 78% of the plaintext
re-chunked, 0% new payload).

**2. How much exact member payload reuse exists in real layers?** Member-level payload reuse (bytes of the new revision whose content digest exists in the base) is 95% (Debian), 99%
(Go), 94% (django), 75% (syn), 68% (tokio), 61% (CPython layer), 64% (pip tail), 51% (apt deps), 22% (Alpine), 28% (grep), 6% (coreutils), 14% (TypeScript, but 100% of its chunks are reusable inside members).

**3. Does member-internal CDC help large changed files?** Yes, where it matters, and it costs little elsewhere. TypeScript (a 9 MB file with small edits, 110 members): whole-member identity 89.7% -> `tarmem-cdc64k` **38.3%**
(= the plaintext floor); synthetic large member with an inserted 200 B: 89% -> 53%; python layers -1.6 to -3.5 points; Alpine -5.3. Where members are mostly small or entirely new it
only adds boundaries: Go +1.6, tokio +0.7. It is one threshold on the same representation (`CDCMin`), not a second subsystem.

**4. Does tar-aware segmentation materially beat plaintext CDC?** On layers and release-to-release archives with unchanged members, by 2-5x (table above: `tar only` vs `plaintext floor`:
Debian 7.5 vs 16.8, Go 15.9 vs 75.5, django 39.9 vs 99.4, CPython layer 40.1 vs 70.9, pip tail 48.4 vs 95.8). On archives where every member changed it does not (grep 99.6 vs 100.0, coreutils 113.1 vs 100.3, lodash 94.4 vs 84.3).

**5. Total composed cost after DEFLATE + tar?** See the table: Debian **9.3%**, Go **19.7%**, CPython layer 42.8%, django 44.6%, syn 46.6%, apt deps 46.5%, pip tail 52.0%, tokio 68.3%, TypeScript 41.4%, Alpine 78.9%; against
raw CDC ~100% and DEFLATE-only 18.6 / 79.3 / 73.6 / 104.2 / 111 / 54.5 / 99.5 / 116 / 43.2 / 84.0. Exact gzip bytes in every case.

**6. What metadata dominates?** `z3tar cost` breaks each composed update down (percent of compressed bytes):

| family | new payload | new headers (+small members) | unit blobs | **references** | inline | skeleton |
|---|---:|---:|---:|---:|---:|---:|
| Debian (cdc64k) | 4.7 | 0.4 | 0.3 | **3.2** | 0.0 | 0.6 |
| Go source (plain) | 1.4 | **5.1** | 1.8 | **10.7** | 0.1 | 0.5 |
| CPython layer (cdc64k) | 36.8 | 1.3 | 1.3 | 2.9 | 0.1 | 0.5 |
| django sdist (cdc64k) | 7.0 | **19.2** | 3.3 | **14.8** | 0.1 | 0.1 |
| tokio crate (plain) | 42.6 | 6.7 | 4.7 | **14.1** | 0.1 | 0.1 |
| lodash | 47.5 | 0.4 | 1.5 | **47.8** | 0.2 | 0.1 |

References (96 B per component, ~2 per member) are the first-order cost wherever members are small or numerous; new header bytes matter in archives whose headers change every release (django 19%, six 10%, Go 5%) and
are immaterial for OCI layers (0.4-1.4%, pip tail 4.0%). Skeleton/recipe is a flat 0.5-0.8%. Exact headers beat any tuning of the reference size first; see G below.

**7. Are PAX/GNU variants problematic?** No. V7, ustar, GNU, PAX prefix entries and base-256 sizes parse and rebuild exactly; PAX doubles the structure bytes (django: 9 981 `x` entries = 25% of the plaintext)
but they are ordinary structure. Whiteouts: layers 3.12.1-3.12.3 carry 12 / 20 / 8 `.wh..wh..opq` opaque-directory markers (zero-size regular files) and 3.12.4+ none; they are plain header-only members,
no special handling and no container-runtime semantics. Nothing the real corpus contains was unsupported; the unsupported classes (sparse, bad checksum, non-zero padding, truncation) fail to plain CDC.

**8. What does range reconstruction require?** (`z3tar range`, last revision of each family)

| | CDC chunks | `tarmem` | `tarmem-cdc64k` |
|---|---|---|---|
| read one member (pieces / bytes / member bytes) | 1.0-1.4 / 125-205 KiB / 6-34 KiB | 1.6-2.0 / **= member bytes** (6.5-82 KiB) | 1.8-8.8 / = member bytes (3.8-649 KiB) |
| 4 KiB tar range | 1 piece, 120-195 KiB | 1.0-2.1 pieces; **0.65-5.7 MiB when it lies in one large member** (TypeScript 5.7, Alpine 2.2) | 1.0-2.0 pieces, 22-123 KiB |
| 64 KiB tar range | 1.4-1.9 pieces, 200-230 KiB | 1.8-21 pieces; 5.6 MiB for TypeScript | 1.6-18 pieces, 85-197 KiB |
| piece index (lengths; offsets are prefix sums) | 0.0% of the tar | 0.0-0.1% | 0.0-0.1% |
| member index (offset + path) | - | 0.0% (TypeScript) .. 1.0% (django) of the tar | same |
| find a member without an index | impossible (headers not addressable) | read ~half of all header pieces (Debian: 2 110 x 512 B) | same |

One member needs only its own pieces (the headline win: CDC chunks amplify a 6-34 KiB member into 125-205 KiB). A tar byte range needs the piece index, and large members need CDC inside (otherwise 5.7 MiB for 4 KiB). Compressed
ranges of a gzip layer go through the existing DEFLATE range index (every unit indexed): MaterializeRange asks for 175-415 KiB of plaintext spans for a 4 KiB range regardless of tar; mapped to pieces
`tarmem-cdc64k` stays at or below the CDC byte volume (Debian 262 KiB vs 331 KiB; Go 231 vs 329) in 2-25x as many (smaller) pieces, while `tarmem` without internal CDC amplifies to 2.9-5.5 MiB when a range
falls in a big member (Alpine, TypeScript). So: the structural index is tiny, the member index is optional, and internal CDC is required for large members.

**9. When should tar transformation be rejected?** `z3tar admit` runs a rule that sees only per-pair features and never encodes anything: predicted tar cost = (1 - exact member reuse) + 2 references
per member / stored bytes + 1%; predicted DEFLATE-only = (1 - plaintext CDC reuse) + 2%; raw CDC = 1; take the smallest. It is not fitted (one reference size, two small margins), and it is
checked against the measured winners:

| set | families | mean regret | misses |
|---|---:|---:|---|
| release tarballs + Debian/Alpine/Go | 11 | **1.0 point** | grep (picked tar 107.1 vs raw 97.7: header churn the estimate ignores); TypeScript (DEFLATE-only 43.2 vs 41.4) |
| python layers | 3 | **0.0** | - |

Reject tar semantics when the predicted cost does not beat DEFLATE-only or raw CDC: exact member reuse of 6-28% of the payload (coreutils, grep) with every header changed, or members so many and small that references swamp the saving (lodash: refs 48-64% of the stored bytes). Alpine (22% reuse) is the marginal case: -5 points against DEFLATE-only, still chosen.
The chooser never looks at the magic; a tar whose members all changed stays raw CDC / DEFLATE-only.

**10. Does this support the generic nested-representation architecture?** Yes, with the costs written down. The same primitives generalised without a new subsystem: content digest (payload identity), CDC (plaintext and inside large members),
component references and representation accounting (`Acct`/`Plan`), exact reconstruction as a checked property of every row. The nested graph worked as designed:
gzip bytes <- DEFLATE skeleton + units + plaintext components <- tar structure + payload components; the plaintext is stored once, as tar pieces. Limits: it only pays when the *semantic* layer actually repeats, each layer adds
references (composition cost 1.8-6.1 points), and member-granular components make reference overhead the new first-order cost, so a production design needs grouped references for runs of small members and an admission rule on member reuse.

## G. Metadata cost, measured before optimised

Per family, exact header bytes as a share of the plaintext: OCI layers 2.0-4.3%, Go 5.7%, crates/GNU tarballs 2.4-6.0%, django/six/lodash **16.8-25.3%** (a PAX `x` entry per member in django and six; lodash is simply 1 049 small files). DEFLATE of the whole structure stream is 0.1-0.8% of the plaintext
and a per-path field-delta model (flag per member, changed field bytes, DEFLATE of new paths; checksum is recomputed) is 0.0-0.3%. What the representation actually pays for new headers is the table above:
0.4-1.4% on OCI layers (not worth a predictor), but 5-19% on Go, django, six, tokio, whose headers change with every release. A header predictor is therefore a candidate for release-archive
families, not for container layers; it was **not built**. Deliberately kept: exact 512-byte headers as ordinary components.

## Not done / limits

* No registry or layer-chain semantics beyond treating whiteouts as ordinary header-only members; no filesystem view.
* Reference costs use the laboratory's 96 B chunkRef; the grouped-reference design that would repair lodash/small-member archives was not built.
* The admission rule is evaluated on the 14 families it was run against (no held-out set); one miss (grep) is the header-churn term it ignores.
* One CDC (the production parameters) and one DEFLATE unit policy (`tok16k`); GNU sparse is out of scope by design.
* Range numbers are lab materialisations of the Z3-08 range index, not a production range GET.

## Tests actually run

Focused during development: `go test ./tarz3` (parser / Split / Join / malformed), `go test ./lab -run Tar` (plain-tar and DEFLATE+tar rows exact, payload reuse under mtime shift and directory rename, non-tar gzip falls back exactly), `go test ./corpus` (new families: deterministic regeneration, validity).
One 20 s smoke fuzz of the new parser (`FuzzParseJoin`: whatever `Parse` accepts must `Split`+`Join` back byte for byte; 810 k executions, no failure). No DEFLATE or Safetensors fuzzing was rerun.
Final: `gofmt -l .` (clean), `go vet ./...` (clean), `go test ./...` once - it exposed one real failure (an existing lab test requires every non-reconstructing row to be named `oracle-*`; the gzip-input tar rows were renamed `oracle-tarmem*`), then `go test ./lab` again (pass; the other packages had passed in the full run and were unchanged).
Root ZeroS3 tests were not run: `zeros3.go` and the root module are untouched.
