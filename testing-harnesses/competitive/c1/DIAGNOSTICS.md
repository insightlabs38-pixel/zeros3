# Supplemental C1 diagnostics

These lanes were added after the primary registration. They preserve the
original result files and are reported as diagnostics.

- SeaweedFS reported an internal filer-log volume allocation failure with
  the original two-volume cap. Retest using eight volumes with the same
  replica and write settings. Verified data operations and configurations
  remain in their original result files.
- Add a Kopia zstd-fastest lane to separate repository compression from
  default snapshot format and chunking.
- Compare ZeroS3 authority archive to archive with generic deltas and
  structural representations disabled. Keep the same input, bases, depth,
  grouping, storage accounting and restore checks. The base CDC lane
  identifies savings shared by the two offline optimizers.
- Add independent Coreutils source and lodash npm release histories. Pin
  downloaded artifact URLs and SHA-256 before comparison. Analyze separately
  from the unchanged 12-family synthetic composite.
- Test a separately generated Snappy Parquet history and an LZ4 RocksDB
  SST history using real encoders. Pin Python packages and verify the
  SHA-256 captured in the run. RocksDB regenerates equal-length SSTs with
  different byte checksums across invocations. SST reproducibility is
  at the workload level, and its raw row results identify the staged files.

- Garage showed a stable 44 ms floor on persistent HTTP range reads.
  The header-signed probe measured a 2.01 ms median with a fresh connection
  and a 44.00 ms median when requesting QUICKACK on the client. The result
  isolates a connection-reuse effect. The precise TCP or server cause
  remains unverified.
- The OpenXet tensor-edit reconstruction failed independently with the
  Xet chunk cache set to 64 MiB and disabled. The official client reported
  an out-of-bounds range during reconstruction. The failing path remains
  excluded from download latency summaries. Repeated failures demonstrate
  incompatibility for the specific pinned local server and client pair.
