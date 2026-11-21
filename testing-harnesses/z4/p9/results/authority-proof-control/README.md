# Authority proof and persistence controls

The ordinary root run on the earlier research checkpoint exposed a
ZN7 proof failure when an input base or imatrix was a pending pin.
The resolver now uses the same verified record overlay as the body
reader. The existing model-history, base-retention, quantizer and
prior-reader compatibility tests passed on the focused rerun.

The persistence simulator now first checks that its migration trace
published a delta. Its missing-fsync control probes the persisted
image before recovery can repair it. The focused rerun identified 22
failing images in the negative control and passed the ordinary crash
images and recovery checks.
