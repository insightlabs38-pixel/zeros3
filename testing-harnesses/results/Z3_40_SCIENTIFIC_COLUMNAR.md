# Z3-40 scientific/columnar shootout — first checkpoint

Status: in progress; characterization only. No promotion or production change.
50 native HDF5/ORC/Arrow files,200 exact source/interior rows. All are explicitly
PRODUCER_WORKLOAD, not natural histories. HDF5 uses h5py3.15.1/HDF51.14.6;
ORC pyorc0.10/PyArrow18.1, Arrow PyArrow18.1. These span distinct native libraries;
the two ORC APIs share Apache ORC foundations and are not independent codecs.
Eight controlled stages: base, one-cell, sparse updates, append rewrite, middle
delete rewrite, metadata-only, repack/remove metadata, restore original geometry.
Two extra HDF5 files exercise actual unlimited-dataset resize.
Native region offsets come from producer/oracle APIs, not a candidate parser yet.
Logical array rows do not claim reconstruction of the original container.
Initial native-region splits are weak; logical-array oracles expose larger gaps.
Next: strict bounded parsers and existing-primitives exact composition for the
strongest feasible profiles, independent references, full costs and range evidence.

Second checkpoint: checksum-validated HDF5 superblock3/object-header2/unpaged
fixed-array framing and bounded Arrow file/stream/FlatBuffers buffer parsing.
Model PLANES uses bounded stored residual blocks; Arrow Zstd reuses an explicitly
limited existing codec builder. No residual-role or decoder contract is weakened.
Nine small native fixtures cover filtered/raw/legacy/unlimited HDF5 and Arrow
file/stream/dictionary/Zstd/opaque-LZ4. All supported fixtures prove full bytes.
300 controlled rows now include native-fine and exact planes/codec candidates.
HDF5 raw8.03MB→4.00MB; Arrow raw9.41MB→3.98MB; Arrow Zstd9.21MB→4.22MB.
These are controlled-history costs, not promotion evidence. Filtered HDF5 remains
opaque (~7% gain); unlimited HDF5 and ORC candidates explicitly retain CDC.
Independent reference files, ranges, memory, hostile fuzz and final report pending.
