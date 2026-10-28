# I1B codec fixtures

Regenerate from `testing-harnesses/z3` with:
`go run ./cmd/i1b-fixtures ../testdata/i1b-codecs`.

`lab-codecs.json` freezes seven lab-proved inputs, recipes and component sets.
The lab's existing Z3RP-v2 envelope is translated to the existing ZS3R-v1
production envelope; only magic/version and derived-child addresses change.
Codec recipes, tables, units and stored/plaintext component bytes do not change.
Three Go gzip producer-mode fixtures are synthetic; four pinned zstdz3 vectors
are reference fixtures. See zstdz3/testdata/MANIFEST for their source provenance.

`nested.tar` is the first `kinds.CompoundChain(32)` stage: fixed library, SQLite
and CAR. `nested.zst` is `zstdz3.Synth(nested.tar)`. Both are synthetic, hermetic
fixtures; the lab producer is never needed by a production decoder.

SHA-256:
- lab-codecs.json: 7a153380c04f2aa07a37f48f47db254135c678914e301a75bb16cdc0e170d535
- nested.tar: 8a0a476ea93abab0c894c8becada835f3d0341ad0ae13b0f35ce3ccc85b99a1f
- nested.zst: 194f54936361f40920e73adc270181837261123bebaf47c59ae6745487a4f447
