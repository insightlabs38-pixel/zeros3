# Z3-39 XZ/package frontier — characterization checkpoint

Stage A in progress; no candidate XZ decoder or production change.
Six pinned real releases: Coreutils9.5/9.6/9.7 tar.xz and Debian Hello
2.10-3/2.10-5/2.12.3-1 deb (ar + control.tar.xz + data.tar.xz).
All downloads <=16 MiB; LZMA oracle memory128 MiB and decoded output128 MiB.
The tar interiors are proved exactly through the existing standalone lab decoder;
compressed sources have independent exact CDC proofs. Interior proof is not
claimed to reconstruct the compressed outer XZ/package byte stream.
First 24 rows: compressed CDC is cheaper than all tested decoded/tar oracles.
Same-input reexports and final evidence/negative bounds checks remain pending.
