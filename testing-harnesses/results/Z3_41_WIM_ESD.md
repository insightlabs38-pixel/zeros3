# Z3-41 WIM/ESD — native identity characterization checkpoint

Status: in progress.24 native-verified controls, four profiles (none/XPRESS/LZX/
solid-LZMS), six tree states. These are synthetic Windows-like filesystem trees,
not related Windows image/update histories. Existing Q5 Go PE outputs are reused.
Tool packages are pinned; actual runtime reports1.13.5 despite Debian1.13.6 naming.
Header, lookup, XML/file metadata, hashes, solid/normal resources and chunk framing
are inventoried by the isolated native oracle. Existing Concat proves the exact
outer bytes independently; no codec executable/analysis closure is used at decode.
120 rows include source CDC, opaque resource/chunk oracles and plaintext-file
oracles. Logical rows do not claim reconstruction of their compressed containers.
LZX opaque chunks3.91MB versus CDC4.68MB; plaintext-file oracle4.03MB. XPRESS
opaque chunks4.23MB versus5.01MB CDC. Codec emulation is not justified here.
Solid LZMS has a large oracle gap but only controlled evidence; do not promote.
CDC+sidecar costs remain additive. Multi-image/integrity/deletion checks and final
report/resource/validation evidence remain pending. No production change.
