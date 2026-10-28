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
