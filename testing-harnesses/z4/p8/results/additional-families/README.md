# Additional P8 benchmark families

The pinned release inputs ran on Linux amd64 with Go 1.27.0. The result file
records wall, CPU, process peak RSS, physical bytes, and verification counts.
Background qualification and other benchmarks ran concurrently. Treat timing
as screening evidence and repeat under quiet conditions for throughput claims.

- LLVM uses six SQLite release amalgamations compiled with Debian Clang 19.1.7
  from the B4 corpus script. PDB uses the five Newtonsoft.Json symbol packages
  from NuGet. Both B4 research runs replayed the release inputs exactly twice,
  hashing original and restored bytes in independent Python processes.
- B3 research ingested three pinned Pythia-14M checkpoints and four pinned
  SmolLM2 GGUF variants. Both runs restored all seven files byte-for-byte
  and retained 482102369 apparent filesystem bytes in their semantic CAS.
- B5 ran its residual coder benchmark twice on a bounded Pythia-14M checkpoint
  pair. The JSON retains the individual codec results and resource measurements.
- Native Git uses hiredis commit 39de5267c092859b4cab4bdf79081e9634b70e39.
  The full pack disables deltas and object reuse. The reordered pack reverses
  the full pack's complete non-delta records, recomputes its Git trailer, and
  uses Git to rebuild its index. Git also generates a delta-pack variant.
  Each production binary replayed all three packs and all three indexes exactly.
  The logs report CDC and additive representation bytes before maintenance.
- The nested Zstd fixture passed two exact production representation replays on
  P8. The larger Zstd and post-maintenance comparative measurements are pending.

Reproduce the research lanes using `z4b4 llvm-store|prog-store` and the matching
fresh replay commands followed by B4's `verify_replay.py`. Run B3 with
`z4b3 ingest -store STORE` and B3's `verify_replay.py`. Use the C1 model downloader
and the B5 `coderbench -maxmb 12 -label pythia14m` path with step3000 and step2000.
For native Git set `ZEROS3_Z3_I1C_REAL_GIT` to the six files and run
`go test -run '^TestRepGitRealReexports$' -count=1 -v .`.
