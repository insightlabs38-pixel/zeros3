# Z3-40 HDF5 / ORC / Arrow IPC frontier

Decisions: HDF5 DEFER; ORC DEFER; Arrow IPC DEFER. Controlled exact gains are
real, but related natural histories and present CDC-plus-sidecar economics do
not justify production promotion. No production/mod/sum/default/kind changes.

## Shootout and provenance
50 native files (NumPy2.3.5): HDF5 h5py3.15.1/HDF51.14.6, ORC pyorc0.10/PyArrow18.1,
Arrow IPC PyArrow18.1. These span distinct libraries; both ORC APIs share Apache
ORC foundations and are not independent compression implementations.
262144-row/four-INT64 controlled data; eight states: base, cell update, sparse
updates, append rewrite, middle delete rewrite, metadata-only, repack/remove
metadata, then original-geometry rewrite. Two files add actual unlimited resize.
These are PRODUCER_WORKLOAD, never natural release histories.
18 separately pinned public references: Arrow integration0.14.1/1.0/C++21,
h5py compound/vlen/big-endian fixtures and Apache ORC Java-version references.
All18 read successfully with native libraries. They are reference fixtures,
not related histories. public.json pins commit URLs, sizes and SHA-256 values.
Public fetch16MiB/file; generated inputs all fit16MiB. Parser source cap128MiB.

## What was implemented
Native chunk/stripe/buffer and logical-array oracles first characterized all three.
Native-only opaque units were generally weak. Deep work selected the two profiles
with large logical gaps and cheap existing exact primitives, not every format.
HDF5: superblock3, compact hard link, object-header2, unpaged fixed-array index;
metadata lookup3 checksums, counts, owners, dimensions and disjoint spans checked.
Raw chunks use existing Model PLANES over bounded stored residual blocks.
Filtered chunks preserve opaque native bytes; no HDF5 filter emulation is claimed.
Unlimited/extensible indexes and old superblocks stay explicit CDC fallback.
Arrow: bounded FlatBuffers vtable/object/vector access, file/stream framing,
dictionary/record batches, buffer spans and footer block identity. All unknown
schema/metadata/framing bytes are preserved. Planes are invertible byte layout,
not a claim to interpret every Arrow type. Big-endian/binary-view fixtures pass.
Zstd buffers compose the existing codec and Model primitive; LZ4 stays opaque.
EncodeZstdLimited mechanically reuses the old builder body with explicit caller
limits; default callers retain their existing recipe/objects/limits.
ORC remains characterization only: native stripe identities do not recover its
logical oracle gap; no speculative RLE/protobuf/codec subsystem was implemented.
No new decoder, persistent primitive, generic BASE or external decode executable.

## Exactness, bounds and evidence
68 inputs produce354 rows:254 original-byte proofs plus400 logical-array interior
proofs. Logical oracle rows do not claim reconstruction of their outer containers.
Per candidate mode:42 direct accepted inputs,26 explicit CDC fallbacks.
Ten independent Arrow references compose exactly;5 older HDF5/3 ORC references
fall back. All cases remain present; no canonical failure was deleted/skipped.
The first composed test caught Model's stored-residual requirement; the adapter
now emits bounded stored blocks without weakening the existing contract.
A small native HDF5 fixture exposed variable filtered-entry widths; parsing now
checks the geometry-derived width and native offsets at both small/large sizes.
Regions16MiB, messages/regions65536, planes8KiB, width<=4096; nested Zstd has
page-sized limits and cumulative declared decode work<=1GiB before decoding.
Full serialized proof and reachability own every read dependency; no hidden objects.

## Eight-state packed estimates, bytes
Costs use the shared production-rule DEFLATE estimator +64B/object, including
serialized roots, codec decisions and full dependency closures. They are not
physical disk benchmarks or zero-cost decode/replay oracles.
|Profile|CDC|Standalone exact candidate|CDC + candidate union|
|---|---:|---:|---:|
|HDF5 raw|8,034,997|3,998,433|12,033,430|
|HDF5 gzip/shuffle opaque|11,147,223|10,356,045|21,503,268|
|ORC pyorc / PyArrow|7,408,039 /7,372,313|CDC fallback|same as CDC|
|Arrow raw|9,405,693|3,982,671|13,388,364|
|Arrow Zstd|9,212,142|4,215,635|13,427,777|

Standalone controlled gains50–57% do not reduce current authoritative-CDC totals.
Filtered HDF5 gains only7%; real histories and broader index/filter coverage remain
required. These results do not unblock or approve the unmerged Model8 draft.

## Authenticated cold ranges and resources
762 exact start/middle/end samples use fresh environments and serialized roots,
without the encoder's plan. Model residuals are fetched/hashed into a per-range
verified source; a corrupt residual now rejects. The old lab Model range helper's
unhashed partial-source behavior is not used as an authentication shortcut.
Partial reads do not claim a whole logical-object SHA proof. Buffer cap64MiB;
source cache bounded128MiB. Compressed regions may rebuild whole native buffers.
Maximum4KiB logical source amplification: HDF5 raw5.90x, filtered24.87x,
Arrow raw5.97x, Zstd177.42x, including roots. These are not physical CAS I/O counts.
Entire proof/price/range shootout:20.09s,131.0MiB peak RSS; no streaming production
memory claim. Resource/source hashes and all samples are in validation.json.gz.

## Gates and continuation
Root suite181.813s; full lab suite/vet and root vet pass. Changed scientific and
Zstd race gates pass; broader legacy kinds race hit default600s timeout without
an assertion failure. That timeout is retained, not relabeled PASS.
Framing fuzz5689 executions/31.085s; checksum/truncation/alias/window/work-budget,
residual-corruption and source-independent exact native fixture checks pass.
Checkpoints11d74c1 characterization, b89832a composition, final authenticated
ranges/cost union/independent references. Sources and repro scripts are pinned.
Continue Q13 from latest main. Keep production drafts80/81 unmerged.
