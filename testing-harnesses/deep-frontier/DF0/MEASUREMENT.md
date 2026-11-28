# DF0 path and cost audit

The baseline and the DF0 benchmark use the same Go readers and native
publication paths. DF0 does not add a format or an optimizer policy.

| Lane | Publication and physical cost | Signed read path |
| --- | --- | --- |
| CDC | Ordinary binary manifests and exact chunks. Finalize the same store as the other lanes. | Ordinary object reader with verified CAS records. |
| Canonical archive | Run incumbent archive optimizer with canonical, splice and fast re-encode options. Preserve actual selected authority forms per object. Charge post-ingest staging, repack, compact, GC and metadata. Report a timeout as an unavailable lane. | The verified authority form selected by the optimizer. Codec work can include inflation and exact re-encoding. |
| GF2 bit patch | `gf2PublishNative` stages independent programs over original CDC source records. Frame digest table and output extent are in the representation recipe. | `bitPatchRead` locates one or more 32 KiB output frames. Each needed frame reads its digest-addressed program and any source members, verifies its target SHA and slices the output. |
| GF7 token translation | `gf7Compile` in the research test file stages a packed index, stored output programs and retained source CDC members. It checks each compiled frame against supplied target bytes. `gf7Publish` then checks engine proof over durable CAS before authority publication. | `deflateTranslateRead` resolves the verified root and a cached expanded index. It constructs member extents, verifies stored programs and source chunks, replays contributing frames and checks compressed-frame hashes. A whole read additionally checks the original object SHA. |

The index carries frame boundaries, program digests, the gzip envelope,
source tables and source positions. It is charged even on an index cache
hit. A program can be a raw frame when it beats a compiled program in the
current pricing heuristic. The encoder's match statistics include blocks
considered for translation. The replay audit counts instructions from
published programs and keeps raw frames separate.

Engine `repSourceObserver` receipts measure stored and logical record
bytes through live signed GETs. They do not count physical device sectors
or namespace reads. The replay audit uses the same `deflateTranslatePlan.read`
and verified store source outside timing trials, with separate measured CAS
and record-verification wall time. Output code emission, token decoding,
hashing, allocations and wait are attributed from sampled native profiles.
The experimental cold path checks file residency instead of treating store
reopen as proof of cold data. GF8's original tests warmed a store before
measuring sparse reads and used only 24 whole requests in a mixed lane.

Each timed lane copies the finalized seed, then primes the requested target
with an exact whole GET for warm measurements. Orders reverse on the
second round and offsets change from a pinned shuffle. Timing includes
request signing, local HTTP and complete response transfer. The allocation
counter is process-wide, so client buffers and transport allocations remain
in the signed lane and are measured identically. Setup and write readback
sit outside that timed interval. In maintenance trials, the isolated
partially live pack and its rewrite are deliberately outside retained
footprint comparisons. Record actual overlap and bytes carried.

The large Python pair exercises the existing 8 MiB compiled-object limit.
The independent confirmation tests examine previously unseen compressed
release bytes using the already frozen method. Source hashes establish
identical acquisitions. Gzip header flags and decoded tar hashes describe
the artifact and do not prove the upstream packager's exact command.
