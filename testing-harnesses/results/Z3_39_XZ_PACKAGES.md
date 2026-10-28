# Z3-39 XZ and package frontier

Decision: NO_XZ_TARGET for the tested real histories. Stage A stops; no Stage B
codec is justified by these measurements. This is not a claim about every XZ
producer or a permanent ban on a materially different corpus/mechanism.
No production code, dependency, format, default or kind number changes.

## Pinned real histories
Coreutils9.5/9.6/9.7 official GNU tar.xz releases (6.0–6.2 MB each), and Debian
Hello2.10-3/2.10-5/2.12.3-1 amd64 deb releases (53–77 KB each).
The debs are actual ar archives with control.tar.xz and data.tar.xz members.
public.json pins six URLs, byte lengths and SHA-256 values; no mutable branch URL.
Fetch bound16 MiB/artifact; decoded member128 MiB; LZMA oracle memory128 MiB.
Actual Coreutils plaintext tars are61–63 MB; Hello combined plaintext266–358 KB.

## Stage A method
Mechanically reused the pinned public fetch driver and existing admission.CDC,
lab.ChunkScaled, kinds.EncodeTarLayer and Capture's serialized proof/accounting.
Four policies: compressed-source production CDC, plaintext production CDC,
plaintext4 KiB CDC, exact tar-member/header/body partition.
Every candidate has independent stored objects/root, SHA/length reconstruction and
complete reachable accounting. Dedup cost includes packed objects +64 B/object,
using the laboratory's production DEFLATE threshold model and serialized roots.
No policy borrows a future root or unpriced hidden object. Stores reset between
proofs while cumulative accounting retains only physical object identities/costs.

Only compressed CDC reconstructs the original outer file. Other rows are decoded
interior oracles; their exactness applies to the tar bytes, not the original XZ.
They omit the future XZ range-coder/filter/decision replay recipe and ar wrapping;
this makes their negative economics stronger, not a completed inverse transform.
No candidate decoder calls xz, liblzma, Python or a producer at decode time.

## Cumulative packed costs, bytes
| Three-release history | Compressed CDC | Plain CDC | Plain4K | Tar members |
|---|---:|---:|---:|---:|
| Coreutils |18,328,891|44,950,237|45,366,307|48,411,614|
| Hello deb |184,300|226,412|295,073|345,895|

Coreutils decoded variants cost2.45–2.64x compressed CDC. Its final-release
increment alone is6.17 MB CDC versus10.71–14.65 MB decoded variants.
Hello costs1.23–1.88x, including the smaller same-upstream packaging update.
Decoded reuse exists but does not pay for losing XZ's strong compression under
ZeroS3's present packed-component rule. No natural-history storage win is claimed.

## Separate reexport control
The exact same Coreutils9.7 tar is recompressed by native XZ Utils5.8.1 at0/6.
Both outputs fit16 MiB and share an identical decoded SHA; they are labeled
REAL_REEXPORT_NOT_NATURAL_REVISION_HISTORY, never extra real release revisions.
Two-source CDC costs20,810,746 B; a single deduplicated plain-CDC oracle costs
15,418,238 B (26% oracle saving), tar-members17,945,656 B.
The first source alone is cheaper than every decoded oracle. The later zero-cost
oracle repeat is expected for identical plaintext; it does not rescue the two
negative natural histories or price an exact LZMA replay decision stream.
No exotic filter, RPM database, 7z codec or range-coder implementation was added.

## Bounds, resource and verification
24 natural rows +8 control rows; each has an exact compressed or interior proof.
Source hash mismatch, source>128 MiB and malformed/overflowing ar spans reject.
Candidate decoder code is existing stdlib-only Concat; no new decoder needs fuzz.
Full laboratory tests/vet pass; root production/mod/sum diff is empty.
The proof-heavy harness peaks592.9 MiB RSS for the natural cohort,609.8 MiB for
the control,10.59s/6.02s respectively. These are harness costs, not a new streaming
XZ decoder's resource measurements. Source/member bounds are explicit above.
The native LZMA export verifies stream end/CRC and rejects extra streams/tails.

Artifacts: testdata/q11-xz/{public.json,characterization.json.gz,reexports.json.gz,
validation.json}; source hashes and validation logs are pinned there.
Reproduce: q11_fetch.py CACHE public.json; q11_prepare_xz.py CACHE;
cmd/z3xzfrontier CACHE/characterization-inputs.json; q11_reexport.py CACHE;
cmd/z3xzfrontier CACHE/reexport-inputs.json; q11_validate.py CLI ROOT CACHE.
Checkpoints:4e2779d pins corpus/characterization; final closes separate controls,
negative bounds, economics and this report. Continue Q12 from latest main.
