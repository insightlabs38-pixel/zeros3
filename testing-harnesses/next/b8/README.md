# B8: cross-format canonical representations

Linux amd64, Go 1.27.0, 4 vCPU Firecracker VM, virtio SSD; base `3baa64e` (B7). Families, configurations and
criteria were fixed before any comparative measurement ([PREREGISTRATION.md](PREREGISTRATION.md), commit
`f133bcb`). Raw rows: [results/](results/) (`tables.py results/*.jsonl` prints every table;
[results/tables.md](results/tables.md) is its output); inputs: [corpus.sha256](corpus.sha256) (published npm,
PyPI and GNU files plus `make_inputs.py`); reproduction: [run.sh](run.sh). Cold reads drop the page cache and
every in-process cache before each read; every read and every original object is checked byte for byte.

**Verdict: `B8_CANONICAL_EFFECTIVE`.** On all three independent real families the archive store is far smaller
than the strongest pre-B8 baseline (B7 with or without existing codec replay): **TypeScript npm `.tgz` −58.8%
(12.63 → 5.21 MB), Django sdist + wheel −61.7% (41.02 → 15.70 MB), Coreutils GNU `.tar.gz` −46.1% (41.21 →
22.20 MB)**, and each is at least 44% below existing replay with B8's own search and pricing fixes (`b7rg`),
so the gain is canonical sharing, not the search. The two constructed wrapper families gain −48.1% (raw/gzip/zstd of one release: 51.77 → 26.85 MB)
and −29.0%; negative controls move ≤ 0.4%. Plans equal the materialized model byte for byte on all 75 optimizer lanes and
every store deep-verifies. The price is read cost: a cold 4 KiB read of a canonical container takes 16-35 ms
(CDC 0.4 ms, existing replay 7-38 ms), a whole read 0.8-2.2 s. The read-priced profiles behave accordingly:
`mixed` keeps 9-48% of the archive gain on four families and rejects it on Coreutils, `hot` stores CDC everywhere.

## Stages and what changed
| | pre-B8 | B8 |
|---|---|---|
| representation | gzip/zstd replay rep whose plaintext is a DERIVED child (a tar rep or a CDC concat) inside the container's own closure | member role **BODY**: the plaintext is an authority body of its own (the *canonical stream*), declared as the rep record's base |
| containers | gzip, zstd | + ZIP (kind 14): entry data replayed over slices of one stream; headers/directory verbatim |
| graph nodes | live contents and records they name | + canonical streams (no namespace entry); a raw object with the same bytes *is* the stream |
| stream forms | — | CDC pin, xd/ZW2 delta against any stream or object, structural rep: everything the graph offers |
| discovery | B7 positional fingerprints over stored bytes | the same index also over decoded streams (streams are ordinary targets and bases) |
| search | Edmonds + local search (+ B7 restricted start) | + group starts (all canonical / all shared-component rep options), exact search of small partitions, canonical-free incumbent, cycle-safe closure update |
| read model | rep options linear, no replay CPU | replay restart + per-byte CPU, wrapper reads mapped onto the stream (warm-up included), table vs unit layout |
| format | — | store feature `canon` (older builds refuse the store) |

## Baseline audit: why B7 kept TypeScript in CDC while B3 measured 23.4%
Three independent causes, each sufficient (TS 5.6.2 / 5.6.3 / 5.7.2 `.tgz`, CDC 12.60 MB modeled):
1. **Eligibility.** `authority optimize` builds gzip/zstd replay candidates only with `-codec-replay` (default
   off); no B6 or B7 lane set it (`b6Lanes`/`b7Lanes`: `AllowRep` only). B3's 23.36% came from `authority migrate
   -codec-replay`, whose group planner admits a kind's members together.
2. **Search.** With replay eligible the graph holds the options and the all-replay selection prices 9.66 MB
   (−23.3%, the B3 figure), yet the B6/B7 search returned CDC (`b7r`: 12.63 MB). A replay rep alone costs more
   than its CDC (4.86 vs 4.24 MB): its value is tar members shared with the other versions, reachable only by
   moving the group together. B6's joint moves group holders of *current* CDC chunks, never options that would
   share *new* components; Edmonds prices a rep as a root edge carrying all its components; the exhaustive
   search ran only as a report.
3. **Pricing.** Rep options were laid out linearly with no replay CPU: a cold 4 KiB read of a replayed
   container modeled ~0.5 ms against ~22 ms measured, so `mixed` would have accepted replay blindly once (1) and
   (2) were fixed.

B8 fixes (2) with group starts and bounded exact search and (3) with replay terms; `b7rg` is existing replay
with those fixes (TS 9.32 MB). That gain belongs to the search and pricing work, not to canonical sharing, and is
reported separately. `b7r` mixed/hot rows already use the B8 read model (B7's own model had no replay term).

## B8-A: reuse oracle (engine-independent, [oracle.py](oracle.py), [oracle.jsonl](results/oracle.jsonl))
Per family: what whole-stream identity and member identity (tar and ZIP entries keyed by SHA-256) could save
without deltas, priced at zlib level 6 per unit; the residual a byte-exact inverse needs (exact-DEFLATE IR
skeleton + units, [irprobe.txt](results/irprobe.txt)); and what B8 stores.

| family | containers MB | decoded MB | distinct streams | distinct members (MB) | stream bound | member bound | exact-DEFLATE residual | B8 archive |
|---|---:|---:|---:|---:|---:|---:|---|---:|
| ts | 12.58 | 67.92 | 3 | 180 (59.96) | 12.65 | 11.17 | 3.0% of the gzip | **5.21** |
| django | 56.88 | 253.85 | 3 + 3 ZIP | 6,204 (49.88) | 57.19 | 14.83 | sdist 3.9%, wheel 3.0% (3,656 entries, 0 fail) | **15.70** |
| cu-gz | 44.89 | 187.02 | 3 | 6,780 (163.59) | 44.17 | 44.41 | 5.6% | **22.20** |
| wrap | 99.36 | 251.97 | 2 | 3,877 (105.99) | 29.65 | 28.52 | gzip 5.6%; zstd l3 ~45% | **26.85** |
| ts-mix | 30.75 | 67.92 | 3 | 180 (59.96) | 12.65 | 11.17 | gzip 3.0%; zstd l3 ~48% | **6.89** |

Identical members explain little of the reuse: consecutive TS and Coreutils releases change their large members
slightly, so member identity is no better than whole-stream compression (11.17 / 44.41 MB). The reuse is
*partial* overlap inside changed members and across reordered containers — which is why B8 makes the stream a
graph node that can be a delta, rather than a set of identical components: B8 stores half of the member bound on
TS and Coreutils, and on Django (where 98% of each wheel's bytes are sdist members) lands within 6% of it while
also paying every recipe, header and directory. The exact-DEFLATE residual (2.9-5.6% for npm, setuptools,
Python zipfile and GNU gzip producers) is what makes the inverse economical; zstd's replay table is not
(below). The oracle selected the families: all five justified implementation; xz (lone) has no exact inverse here.

## Architecture
**Canonical stream.** The decoded plaintext of a codec container, identified by the SHA-256 computed while the
container is replayed — never a header, name or reported size. It is a *body*: an authority record keyed by that
digest with no namespace entry, alive only while a record names it as a base (reachability, gc, B4 pins and
retirement are the existing base machinery, unchanged). It takes any graph form: CDC pin, delta against another
stream or a live object, structural rep. A raw object whose bytes equal a stream *is* that stream (one node).

**Wrapper.** The container's record is a rep (`authFormRep`) whose representation ends in a **BODY** member
(`0x04 | SHA-256 | length`: exactly that body's bytes). The record declares the body as its only base; its depth
is 1 + the stream's. The wrapper's own components are the replay recipe: gzip header/trailer, IR skeleton and
units (the exact-DEFLATE corrections), seek index; zstd frame skeleton, sequence table, seek and reach tables;
ZIP RAW segments and per-entry IR. Containers of one plaintext share one stream (wrap: the GNU `.tar.gz`, the
`.tar.zst` and the raw `.tar` of 9.7 are one 63 MB stream, `shared` 2).

**ZIP (kind 14).** Segments: RAW (local headers, data descriptors, gaps, central directory, verbatim), STORED
entry data (canonical bytes as is), DEFLATE entry data replayed by the gzip IR from that entry's slice of the
stream. The stream concatenates the decoded entries in file order. Entries that do not replay exactly
(encryption, other methods, Zip64, inconsistent sizes) stay inside RAW, so any well-formed archive reproduces;
a damaged directory is refused. Analysis runs four workers over bounded batches; range reads replay only the
entries they overlap (from the entry start) through a read-ahead window bounded by the range's plaintext.

**Invariants (fail closed).** BODY is legal only in gzip/zstd/ZIP reps; a rep record's bases must equal its
representation's body references exactly (checked at publication, reachability and read); body references
resolve only to declared bases and only in an authority context (a Z3 manifest root with one is a fault; a plain
`repEnv` refuses); the streamed body is hashed against its address; bases ⇔ depth ≥ 1; `canon` is raised in
FORMAT.json before the first such record, so older builds refuse the store instead of reading records as corrupt.
Exactness is always proven on the original object: planning builds prove the whole container from the in-memory
stream, apply rebuilds the representation (root and stream digest must equal the plan) and proves the record
through the production authority reader before publishing.

**Reads.** Whole reads stream through the representation's root reader with an authority environment of their
own (gzip and zstd cut at restart points across four workers); ranges use the B2 seek index with the plaintext
window read from the stream body by range; ZIP ranges replay overlapping entries. A stream body is shared by the
range workers through one serialized reader per stream.

## B8-D: discovery through canonical content
Canonical analysis runs at optimization time, lazily per container, in memory (`repConstructCanon` with an
in-memory sink: nothing is staged; the plaintext lives in a temp file served to the planner's readers through a
synthetic manifest). Streams then join B7's positional-fingerprint index as ordinary targets and bases, so the
same posting index finds stream↔stream, stream↔raw-object and cross-container (wheel↔sdist) relationships; every
candidate is still verified by the matcher and priced by its encoded envelope. Similarity is never identity:
identity is only the replayed plaintext's SHA-256. Costs per family (b8 archive): analysis and proof 6 s (TS),
24 s (Django, 6 containers, 254 MB decoded), 34 s (Coreutils, 187 MB), 25 s (wrap); temp files hold the decoded
streams (peak = decoded bytes of the distinct streams, 68-254 MB) and are removed when planning ends. A stream may
build only on a stream earlier in the policy's version order: streams are required by nothing but their users,
and a cycle among them would keep itself required (found by the eval invariant test, below).

## B8-E: optimizer integration
* **Closure accounting.** A stream is paid once however many containers name it; each wrapper pays its recipe,
  record file and writes; a stream nothing requires costs nothing and is never published. Writes are charged
  only for required nodes (previously an unrequired intermediate's selection counted). The closure update is
  cycle-safe: a node is detached before its old option is released (a transient A↔B selection cycle routed the
  release back into the node and double-counted; B6/B7 never met it because every cycle there had a live node).
* **Depth.** The policy depth limits delta chains of streams; the wrapper's replay is priced by the read model, not
  counted as a policy level (hard limit 4 still counts it). Without this Django's wheels could not build on the
  sdist stream (20.6 → 15.7 MB).
* **Search.** Group starts, exact search of partitions with ≤ 16 Ki (archive) / 1 Ki (read-priced) option
  combinations, and an incumbent: the full search must beat the plan found with canonical options excluded, so
  more options never yield a worse plan (tested on all profiles). The B7 search (`b8s`) reaches the same archive
  plans on every family; the group starts matter for existing replay (`b7r` → `b7rg`: TS 12.63 → 9.32 MB,
  Django 41.02 → 34.44, wrap 51.78 → 44.60).
* **Accuracy.** Plan bytes equal the materialized model after gc + repack + compact on all lanes.

## Materialized stores (allocated MB after optimize + gc + repack + compact; [lanes.jsonl](results/lanes.jsonl))
Archive profile:

| family | cdc | b7 | b7r | b7rg | b8s | **b8** | b8-nodelta | b8-nozip | b8 / pre-B8 best | b8 / b7rg |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| ts | 12.63 | 12.63 | 12.63 | 9.32 | 5.21 | **5.21** | 9.30 | | **0.412** | 0.558 |
| django | 55.66 | 41.02 | 41.02 | 34.44 | 15.70 | **15.70** | 38.96 | 23.03 | **0.383** | 0.456 |
| cu-gz | 44.77 | 41.21 | 41.21 | 41.21 | 22.20 | **22.20** | 41.21 | | **0.539** | 0.539 |
| wrap | 51.77 | 51.77 | 51.78 | 44.60 | 26.85 | **26.85** | 31.98 | | **0.519** | 0.602 |
| ts-mix | 12.75 | 12.75 | 9.70 | 9.70 | 6.89 | **6.89** | 9.05 | | **0.710** | 0.710 |
| random | 50.45 | 50.45 | 50.45 | | | 50.45 | | | 1.000 | |
| lone | 6.24 | 6.24 | 6.24 | | | 6.22 | | | 0.996 | |
| zst19 | 13.48 | 13.48 | 13.48 | | | 13.48 | | | 1.000 | |

Mixed profile (read weight 20 B/µs, whole 1 B/µs; production `newest` order) and hot:

| family | cdc | b7 | b7r | b7rg | b8s | **b8** mixed | b8 / pre-B8 best | b7rg / **b8** hot |
|---|---:|---:|---:|---:|---:|---:|---:|---|
| ts | 12.63 | 12.63 | 12.63 | 12.63 | 9.04 | **9.04** | 0.716 | 12.63 / 12.63 |
| django | 55.66 | 41.07 | 41.07 | 41.07 | 32.82 | **32.82** | 0.799 | 55.66 / 55.66 |
| cu-gz | 44.77 | 41.21 | 41.21 | 41.21 | 41.21 | **41.21** | 1.000 | |
| wrap | 51.77 | 51.77 | 51.77 | 51.77 | 26.85 | **26.85** | 0.519 | |
| ts-mix | 12.75 | 12.75 | 9.70 | 9.70 | 8.80 | **8.80** | 0.907 | |
| random / lone / zst19 | 50.45 / 6.24 / 13.48 | same | | | | same | 1.000 | |

Under `mixed` existing replay is rejected on every real family (its read cost buys 23% at best) while the
canonical form, which buys 45-60%, is accepted where the trade pays: the profile keeps the newest version of each
history CDC, so only older releases (and every wrap wrapper, whose stream is shared) become canonical. `hot`
stores CDC everywhere (its read budget is 2.5× the CDC read).

Attribution (archive): existing replay is worth 0-23% when the search can find it (`b7rg`); pure stream sharing
(`b8-nodelta`: streams only as CDC) adds nothing on single-wrapper histories (TS −0.2%, Coreutils 0) and 28% on
wrap, where three wrappers hold one plaintext; **deltas between streams are most of the gain** (TS 9.30 → 5.21,
Coreutils 41.21 → 22.20, Django 38.96 → 15.70); the ZIP adapter is a third of Django's (23.03 → 15.70: the wheels
become deltas against the sdist stream, cross-container sharing no byte or stream identity exposes). Selected
graphs (archive): one stream per family is CDC, the others are deltas (Coreutils windowed ZW2), all containers
are canonical wrappers; depth ≤ 3 (stream chain 2 + wrapper).

Migration (b8 archive, wall / CPU s, peak RSS): TS 18 / 39 s, 300 MiB; Coreutils
138 / 185 s, 904 MiB; Django 99 / 229 s, 1,188 MiB; wrap 76 / 97 s, 828 MiB. Peak allocated before reclamation: 1.6-2.5× the CDC store it started from (wrap 70.7, Coreutils 113.8, Django
122.8 MB: stream chunks and recipe components written next to the CDC chunks they replace, until gc);
process writes include the planning temp streams.

## B8-H: reads ([reads.jsonl](results/reads.jsonl.gz); cold unless noted; medians, ms; model = optimizer read model)
| family | form | 4 KiB | 64 KiB | 1 MiB | whole | 4 KiB model |
|---|---|---:|---:|---:|---:|---:|
| ts | CDC | 0.41 | 0.56 | 3.2 | 18.5 | 0.50 |
| ts | existing replay (b7rg) | 21.8 | 44.3 | 192 | 716 | 16.5 |
| ts | canonical gzip (b8) | 31.3 | 67.1 | 229 | 782 | 19.6 |
| django | canonical gzip sdist | 32.3 | 86.0 | 240 | 1,721 | 32.8 |
| django | canonical ZIP wheel | 15.7 | 25.0 | 190 | 1,372 | 17.7 |
| django | existing replay sdist | 37.6 | 66.9 | 184 | 1,380 | 16.8 |
| cu-gz | canonical gzip | 30.7 | 72.6 | 211 | 2,158 | 33.3 |
| wrap | canonical gzip / zstd | 15.4 / 68.3 | 42.4 / 78.4 | 293 / 225 | 1,831 / 1,084 | 20.8 / 73.4 |
| wrap | existing replay gzip / zstd | 16.5 / 26.8 | 72.7 / 38.0 | 409 / 145 | 2,637 / 627 | 16.6 / 23.7 |

Canonical sparse reads cost about what existing replay costs (the predictor restart dominates) plus the stream's
own read: 1.4× on TS (its streams are depth-2 deltas), equal on Django sdists and wrap gzip, 2.5× on wrap zstd
(the zstd model window of 2 MiB is now read through a delta). ZIP 4 KiB reads (15.7 ms) replay one entry. Whole
reads run four restart workers (TS 0.78 s; 3.2 s before parallel restarts for body-backed gzip). The read model
predicts gzip/ZIP/zstd sparse and whole reads within ±40% except 64 KiB gzip (under by ~1.7×), which is enough to
rank options at the profiles' weights; its replay constants are the a-priori B2 values, not refitted.
Mixed stores ([reads-mixed.jsonl](results/reads-mixed.jsonl.gz)): the canonical containers the profile kept read
like the archive ones (TS 4 KiB 19.0 ms, wrap 30.0 ms); Django's mixed store keeps the newest sdist and wheel CDC
and its 4 KiB median is 0.96 ms (existing-replay mixed store: 0.58 ms, all CDC/deltas); Coreutils is unchanged.

## Concurrent readers (8 closed-loop, 75% 4 KiB / 25% 64 KiB, 10 s, cold start, both lane orders; [concurrent.jsonl](results/concurrent.jsonl))
| family | lane | reads/s (two orders) | p50 ms | p99 ms | CPU s |
|---|---|---|---:|---:|---:|
| ts | cdc | 205,185 / 208,531 | 0.007 | 0.44-0.49 | 34 |
| ts | existing replay (b7rg) | 188 / 184 | 32-35 | 189-200 | 39 |
| ts | canonical (b8) | 151 / 155 | 42 | 170-188 | 39 |
| django | cdc | 50,361 / 51,008 | 0.036 | 1.9 | 37 |
| django | existing replay (b7rg) | 278 / 268 | 14 | 163-168 | 39 |
| django | canonical (b8) | 182 / 183 | 30-31 | 202-209 | 39 |

Replayed storage is CPU-bound: four cores serve 150-280 replayed sparse reads per second against 50-200 thousand
from CDC. Canonical reads are 20-35% slower than existing replay under load (the stream's own reconstruction
comes on top of the predictor restart), consistent in both orders. This is the cost the profiles price.

## Negative findings
* **zstd replay recipes are large.** The exact zstd replay table of python-zstandard level 3 output is ~45-48% of
  the compressed size (wrap: 4.8 MB per 10.6 MB `.tar.zst`; ts-mix 1.9 MB per 4.0 MB), against 3-6% for DEFLATE.
  Canonical zstd still pays when the stream is shared or a delta (the container is replaced by its table), but
  a lone zstd container never would, and level 19 is refused as before (zst19: 2 of 2 rejected, store unchanged).
* **Coreutils `.tar.gz` under mixed: rejected.** 31 ms per 4 KiB and 2.2 s per whole read against 0.4 / 39 ms
  outweigh 19 MB at mixed weights; the archive profile takes it.
* **Pure stream sharing is rare in real histories.** Each release has its own plaintext; without deltas between
  streams (`b8-nodelta`) TS, Django sdists and Coreutils gain nothing over existing replay. Byte-stream
  equivalence pays only when one content is published in several wrappers (wrap).
* **Existing nested replay was already member-structured.** A gzip/zstd replay rep's plaintext was a tar rep with
  member components, so identical tar members were already shared across containers and versions (`b7rg`); B8's
  gain is partial overlap (deltas), wrapper-independent identity and ZIP, not member identity.
* **Engine pathology exposed.** Deep verify of a stream stored as a windowed delta over another windowed delta
  re-decoded a base window at every COPY jump (> 8 min for one 10 MB `.tar.gz`); windowed bases below the top now
  run sub-range programs in sequential passes too (10.6 s). Similarly, the first ZIP read-ahead loaded 16 MiB for
  a 4 KiB read (241 ms); it is now bounded by the plaintext the range replays (15.7 ms).
* **xz and other unsupported wrappers** (lone: Coreutils `.tar.xz`) keep the generic byte path.

## B8-G: small-record allocation (measured model, not implemented)
Canonical stores add few records (TS: 6 record files, 33 KB allocated of 5.2 MB; Django: 12), so grouping does
not matter for B8's own output; its lever is small-file histories. A model of a grouped container (each record
priced at its exact bytes plus 72 B: a 64 B entry header and amortized group slack), planned on B6's Coreutils
per-file history (1,211 files, [recgroup.jsonl](results/recgroup.jsonl); planning only, nothing written):

| record pricing | archive MB (deltas) | mixed MB (deltas) |
|---|---:|---:|
| 4 KiB allocation floor (today) | 4.04 (89) | 4.24 (32) |
| grouped container model (exact + 72 B) | **2.46** (525) | **3.62** (74) |
| exact bytes (B7's upper bound) | 2.40 (529) | 3.59 (76) |

The realistic grouped model keeps 96% of B7's upper bound (−39% archive, −15% mixed): the per-entry header costs
little; the allocation floor is the whole problem. This is the strongest small-file storage lever measured and
justifies its own milestone. It was not implemented in B8: it is a new persistent container behind every
record-file owner (publication, `authLoad`, retirement, B4 epoch reclamation, transfer, revert, repack of the
container itself, torn-tail recovery and a feature gate), and no part of the cross-format work needed it.
Design for that milestone: an append-only `bodies/group-<n>.zbg` of framed entries (magic, content SHA-256,
generation, length, CRC-32C, record bytes), immutable once sealed at 1 MiB; an index entry replaces a body file
by an atomic rename of a small pointer map published like the namespace checkpoint; retirement writes a tombstone
frame and a sealed group is rewritten (copy live entries, publish, retire old) by the B4 epoch machinery like a
pack; a torn tail is truncated to the last valid CRC at open; feature `recgroup`.

## Scale (synthetic, planning only; [scale.jsonl](results/scale.jsonl))
N releases of a 9-file tree as `.tar.gz` (levels 1/6/9 rotating) and every third also a ZIP:

| releases | containers | streams | nodes / options | canonical s | candidates s | search s | wall s | peak RSS MiB | CDC → planned MB |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|
| 8 | 11 | 11 | 22 / 192 | 0.8 | 9.0 | 0.009 | 9.0 | 245 | 3.74 → 0.71 |
| 32 | 43 | 43 | 86 / 1,078 | 3.0 | 37.6 | 0.16 | 37.8 | 449 | 13.34 → 1.81 |
| 96 | 128 | 128 | 256 / 3,489 | 9.7 | 127.7 | 0.63 | 128.5 | 990 | 39.14 → 5.11 |

Canonical analysis is linear in containers (~76 ms each here); candidate encoding dominates and is linear too
(the B7 shortlist bounds bases per target); search stays below a second though the whole history is one
partition (exact search is skipped beyond its budget). RSS grows with the in-memory delta indexes of the decoded
streams held during candidate encoding (~6 MiB per container at this size); this is a synthetic planning
measurement, not a deployment validation.

## Correctness and recovery evidence
`zeros3_b8_test.go`:
* `TestB8CanonicalCrossFormat`: a `.tar.gz`, the raw `.tar` of the same bytes, a ZIP of the same files (reverse
  order, stored and deflated entries, data descriptors, a duplicate name, a comment) and an edited release's
  `.tar.gz` (another level): analysis shares the raw tar as the gzip's stream; the plan applies, equals the
  materialized model, is byte-exact (whole, 4 KiB, 64 KiB, 1 MiB and tail ranges) after apply, gc + repack +
  compact and reopen; deep verify passes; `canon` is raised; a rerun changes nothing; deleting the raw tar keeps
  the stream it was alive for the containers (exact after reclamation, verify passes).
* `TestB8ZipExact`: 200 random ranges around every segment boundary and whole reconstruction exact; a store-only-
  empty archive is refused; reconstruction without a body resolver refuses; a damaged stream never reproduces the
  archive; truncations at 4 points are refused.
* `TestB8ZipTableFuzz`: 400 damaged tables never panic and never parse with inconsistent totals.
* `TestB8RecordShape`: bases without depth and depth without bases are refused by the record parser; a rep whose
  base is not its body reference is refused at publication; with the stream's record removed, reads fail closed
  (no other bytes) and deep verify fails; restored, reads are exact.
* `TestB8EvalCycles`: streams never name each other; 1,500 random moves including cycles through live containers
  keep the incremental closure (bytes, writes, requirements, references, reads) equal to a scratch evaluation.
* `TestB8IncumbentAndProfiles`: canonical options never plan a worse objective than the same run without them
  (archive, mixed, hot); hot selects no replayed option; a fake gzip and random data keep CDC.
* `TestB8CrashMatrix`: **23 real SIGKILL cuts** of a canonical optimize at every occurrence of stream chunks
  flushed, record written, record renamed and optimizer-before-publish: after each, every acknowledged object is
  exact, reclamation retires what the cut left (a stream published before its container), deep verify passes, a
  rerun completes the plan and a further run changes nothing.
* `TestB8OnlineMaintenance`: B4 online passes over a canonical store after deleting the raw tar (a stream) and a
  container: readers stay exact through every pass, records nothing needs are retired, deep verify passes, reopen.
* `TestB8ConcurrentReads`: eight readers of random ranges and whole objects over canonical gzip and ZIP containers
  sharing streams stay exact (also under `-race`).
* Existing tests updated: kind 14 is assigned (wire-number tests); `TestAuthorityNestedRangeExecution`'s reference
  (frame reconstruction over a delta base) got cheaper, so its 2× ratio became strictly-less.

Campaign: 75 optimizer lanes and 7 CDC lanes, all deep-verify after reclamation; plan = materialized
model on every optimizer lane; 1,360 cold/warm/whole reads byte-exact. Executed:
`go test -run '^(TestAuth|TestAuthority|TestRep|TestB3|TestB4|TestB5|TestB6|TestB7|TestB8|TestPack|TestRepack|
TestCompact|TestLocality|TestXDelta|TestP8|TestCoalesc|TestZW|TestWindow)'` (646 s, pass); the root suite once (`go test .`): pass (1,115 s).
`toc_check` passes on the regenerated source map. Not run: T1 qualification, exhaustive fuzzing, external
compatibility harnesses, `-race` beyond `-race` on `TestB8ConcurrentReads`, `TestB8CanonicalCrossFormat`, `TestB8ZipExact`, `TestB8RecordShape`,
`TestB8EvalCycles`, `TestB8OnlineMaintenance` (513 s, pass). Untested: ZIP64 and encrypted archives (refused or RAW by
construction, no fixtures), zstd dictionaries (refused by the existing parser), multi-member gzip (refused).

## Limits and future work
* Read cost is the boundary: canonical containers serve archive and cold tiers; hot data should stay CDC (the
  optimizer enforces this through the profiles, not a rule). A verified decoded-window cache shared across
  readers, or periodic restart snapshots of the stream, would cut the repeated predictor restarts.
* zstd needs a cheaper exact inverse (a residual against a re-encode at the detected level, or literal/sequence
  modeling) before lone zstd containers can pay.
* Streams are analyzed every run (6-34 s per family here); a persisted stream identity per container (content →
  stream SHA, recipe root) would make reruns and online maintenance planning incremental. Planning holds the
  decoded streams in temp files (up to 254 MB here); large histories need a bounded spill policy.
* Tar-member normalization: tar streams that differ only in member order or headers already delta well; a
  member-sorted canonical form was not needed on this corpus. OCI layers (`.tar.gz` blobs) and Python wheels
  of other projects are the natural next families; nested containers (a `.tar.gz` inside a tar) still use the
  nested replay path, not canonical streams.
* Grouped record files remain the strongest small-file lever (B8-G); B8's own output creates few records.

## Reproduce
```sh
bash testing-harnesses/next/b8/run.sh WORK     # needs root (drop_caches), python-zstandard 0.25.0, ~8 GB
zeros3 authority optimize -store DIR -canonical [-apply] [-profile archive|mixed|hot]
```
