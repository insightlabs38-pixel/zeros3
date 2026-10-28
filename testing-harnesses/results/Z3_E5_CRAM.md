# E5 CRAM — characterization checkpoint

E5 remains active. Production untouched; native HTSlib is producer/oracle only.
No new CRAM parser, codec or reference/base primitive is introduced.

Pinned HTSlib 1.21 source archive provides four native CRAM reference fixtures,
three FASTA references and a 1,000-read SAM. Controlled scaling to 32,000 reads
produces 18 exact native workloads over CRAM 3.0/3.1/reference-free 3.1 and six
edit/repack states. All 576,768 generated alignment-field comparisons pass.

Native API oracle locates 2,628 encoded blocks, independently checks compressed
payload bytes against source spans and verifies block CRC32 where present.
Existing Concat serialization retains headers/unknown bytes; no native decoder
or FASTA reference is needed to reconstruct the original CRAM bytes.

Source CDC / fine CDC / native blocks, cumulative packed bytes:
3.0: 487,391 / 311,633 / 331,411;
3.1: 498,114 / 325,454 / 344,104;
3.1 no-ref: 545,142 / 393,643 / 404,052.
Fine CDC already beats native grouping in every generated profile. Additive
closures all lose. Public fixture CDC 29,309 B beats native block 48,921 B.

One public Java fixture is a retained negative: seven H/unsigned-array auxiliary
tag-type conversions preserve underlying bytes but do not restore original SAM
tag types/values. It is never counted as a semantic PASS. Other original-SAM
comparisons remain strict; range.cram has native decode evidence only.
Final validation, resource/provenance closure and the campaign summary remain.
