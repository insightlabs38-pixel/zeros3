# Z3-I1B — reserved Gzip/Zstd production codecs

**Verdict: I1B_COMPLETE.** base `4f97c7beeaad542db854a54aa874f87a6dc6d5ff`.
Kinds 6/7 retain I0 reservations. ZS3R/ZS3T, store level and default off unchanged.
All integration gates pass; persistent bytes/default mode remain compatible.

## Mechanical port and fidelity
Streaming DEFLATE/IR and Zstd are extracted from `z3/{deflate,ir,zstdz3}` by
`z3/cmd/prodcodecport`: reachable declarations, receiver methods for interface
dispatch, package initialization, prefixed globals, flattened package selectors.
Normalized generated declarations compare byte-for-byte with the production port.
Adapters are shell-extracted from `repz3/kinds/{gzip,zstd}.go` and readTable;
only Call/source/error boundaries are adapted. No codec algorithm rewrite.
Initial extraction omitted init functions; fixed in the tool, not hidden by
removing a fixture. All seven fixed fixtures now decode exactly.
Lab Z3RP v2 and production ZS3R v1 already differ. `cmd/i1b-fixtures` proves each
original lab bundle, then translates only envelope magic/version and child
addresses. Codec recipes/tables/units/stored components remain unchanged;
production checks recipe equality, wire roundtrip, length, SHA-256, bytes and
reachability. Evidence: three gzip producer modes (SYNTHETIC_FIXTURE), four lab
Zstd vectors (REFERENCE_FIXTURE). No natural-history claim.

## Streaming analysis and bounds
Plaintext is staged to temporary files; units go directly to CAS, unit metadata
and skeleton stay bounded. The analyzer never materializes whole-stream tokens
or plaintext. Temp files are not namespace roots and are removed on return.
Reuse existing tar/SQLite/CAR analyzers, production CDC for other plaintext,
and serialized DERIVED children. Analysis depth ≤3; decode hard limit stays 4.
Compressed/decoded/child analysis charges the existing cumulative 1 GiB budget.
Gzip retains one-member/header/trailer and bounded predictor-profile retries;
unsupported/multi-member/trailing/checksum cases fall back to CDC.
Zstd retains dictionary-free standard frames; 16 MiB windows, ≤4096 frames,
≤65536 blocks, ≤32 MiB IR, ≤1 GiB plaintext and existing expansion ratio bounds.
Dictionary, magicless, excessive-window and unsupported forms fall back.
Descriptions are capped at min(64 MiB, compressed bytes +64 KiB); tables ≤32 MiB,
≤65536 units of ≤1 MiB each, Gzip skeleton ≤4 MiB and ≤256 MiB declared plaintext.
Unit count must match the Gzip skeleton. Count-allocation regressions pass.
No planner state.
A shared-table first-use race was found in the lab FSE lazy encoding index.
Lab and production now publish predefined tables fully initialized; dedicated
16-way first-use race test passes. Per-request tables remain request-owned.

## Composition, reads, malformed state
Gzip/Zstd→tar→SQLite/CAR reconstruct exactly before/after compact/restart.
Cold internal child ranges include hierarchy reads: SQLite amplification
2.40×/1.09× at 4/64 KiB; CAR 2.39×/1.03×, for both codecs;
public S3 ranges remain compressed-object byte offsets and use authoritative CDC.
Missing serialized codec child fails GC closed while CDC whole GET stays exact.
Twelve codec crash points leave valid existing objects and absent/orphan roots;
GC cleans orphans. Injected temporary-directory failures never fail PUT.
Unsupported dictionary/window/magicless/truncated/CRC/multi-member controls pass.
Default-depth analysis of five compressed layers remains ≤3, exact on replay.
No test removed: rejection still tests reserved Git 4; kind-number tests now
activate 6/7 and assert their exact numbers. Namespace interruption reused Q1's
deterministic fetch gate after exposing the same pre-existing fixed-delay race.
Decoder fuzz 25 s: 125,512 PASS; tightened-bounds repeat 123,551 PASS.
Analyzer fuzz 25 s: 26,328 PASS.

## Bounded large case and economics
Compiled test binary (compiler excluded), 64 MiB SQLite-shaped stream, seed
1411/1412, generated and compressed progressively; no whole-input test buffer.
Stored-DEFLATE gzip: 67,114,009 compressed bytes, 16,641 components, exact proof,
1.5 s end-to-end and peak RSS 50,168 KiB. Separate best-speed negative: 17,831,195
compressed bytes hits the lab predictor work budget at unit 108; CDC fallback
verifies the complete object, peak 19,840 KiB. The negative case is retained and
classified, not replaced by the successful producer. No budget was loosened.
Four-revision compound tar history: fixed library/CAR, sparse SQLite edits,
Go gzip best-speed and local zstd -3 producers; SYNTHETIC_FIXTURE only.
Measurements include authoritative CDC and sidecars; first-sight storage is
additive, not a net saving. Four-revision CAS payload totals (root bytes separate):
| codec | first CDC / CDC+rep | history CDC / CDC+rep | first / history sidecar |
|---|---|---|---|
| gzip | 748,268 / 1,498,219 | 1,570,553 / 2,350,405 | 749,951 / 779,852 B |
| zstd | 744,595 / 1,304,120 | 1,555,957 / 2,145,311 | 559,525 / 589,354 B |
Roots add 430 B/revision (1720 B total). Later sidecar marginal growth ≈10 KiB/rev.
Packed whole-read medians, five reads, CDC/direct decoder: gzip 0.80/8.10 ms,
zstd 1.15/20.30 ms. Public whole GET retains Q1's CDC policy.
No first-sight net-storage or real-history speedup claim is made. The mature
codec research remains Z3_05..08 and Z3_25_ZSTD_EXACT_REPLAY.md on main.

## Final gates and source audit
Final race 49.5 s PASS; lab deflate/IR/zstd/repz3/kinds PASS, lab vet PASS.
Expanded black-box (codec fixtures included): compat 54, bundles/replication 24,
lifecycle 32, crash 24 PASS. Old roots, wire goldens and default behavior preserved.
Full repository suite 206.4 s PASS; root vet/gofmt PASS.
Production LOC 27,584 → 36,146 (+8562); +297 function/method declarations:
272 extracted codec functions, 13 extracted adapter helpers, 12 boundary helpers.
The reachable-declaration extraction avoids an entire lab-module copy. The small
boundary streams to existing CAS/decoders instead of introducing another runtime.
Production GoFiles=[zeros3.go]; non-stdlib dependency list=[zeros3]; root mod files
unchanged. No external executable at decode; optional producer is measurement-only.
No new kind number, wire, default, protocol or persistent planner decision.
