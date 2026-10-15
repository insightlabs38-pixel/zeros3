# Real-world samples (opt-in)

`sources.json` pins public research samples by URL, size and SHA-256 with their upstream reference and license.
Nothing is committed or fetched by tests.

```sh
go run ./cmd/z3fetch -list
go run ./cmd/z3fetch -group six-wheel  -out "$TMPDIR/rw"      # 5 wheels, ~55 KB (ZIP/DEFLATE from pip tooling)
go run ./cmd/z3fetch -group six-sdist  -out "$TMPDIR/rw2"     # 5 sdists, ~170 KB (tar.gz from Python's zlib)
go run ./cmd/z3fetch -group pythia-14m -out "$TMPDIR/rwp"     # 5 safetensors checkpoints, 28 MB each (140 MB)
go run ./cmd/z3lab -corpus "$TMPDIR/rw"
```

The cache defaults to `~/.cache/z3-realworld` (override with `-cache`); a hash or size mismatch discards the download.
Groups: `pythia-14m` (EleutherAI/pythia-14m, Apache-2.0, training steps 1000/2000/3000/4000/143000, pinned commits),
`six-wheel`, `six-sdist` (PyPI `six` 1.12-1.16, MIT).

Candidates not yet pinned or verified (next batches): small GGUF revisions (e.g. a tiny llama.cpp test model across
quantizations), related PE and ELF builds (consecutive point releases of one static binary, e.g. busybox or a Go
toolchain `gofmt`), larger gzip producers (go.dev release tarballs publish SHA-256 in `/dl/?mode=json`, 60-80 MB each),
and PyTorch `.bin` checkpoint revisions from the Hugging Face repositories already used above.

## Blockchain archives (Z3-14 / Z3-15)

* `car.json` - ipld/go-car CAR samples (CARv1, CARv2 with and without index, two malformed). `go run ./cmd/z3car fetch` -> `~/.cache/z3-realworld/car` (override `Z3_CAR_CACHE`).
* `erae.json` - ethPandaOps EraE archives (Sepolia epochs 0-3, mainnet epochs 0-1, 3.4-4.1 MB each). `go run ./cmd/z3era fetch` -> `~/.cache/z3-realworld/erae` (override `Z3_ERAE_CACHE`).
* Solana testnet incremental snapshots are not pinned: the RPC node serves only the current one (`/incremental-snapshot.tar.bz2` redirects to a file that disappears within minutes), and a full snapshot is ~9 GB. Names, sizes and SHA-256 of the measured pair are in `results/Z3_15_MERKLE_STATE.md` (names, sizes, SHA-256).

## Git object sets (Z3-16)

* `git.json` - pinned public repositories (lua/lua, ocornut/imgui, microsoft/vscode-codicons) and this repository, by tip SHA and
  shallow depth. `go run ./cmd/z3git real` fetches each tip by SHA into a bare cache under `~/.cache/z3-realworld/git` (override
  `Z3_GIT_CACHE`, hard cap `-max-mib`), then produces every pack locally with `git pack-objects --threads=1`; no server pack is used.
  The local `zeros3` entry needs `Z3_ZEROS3_REPO=<checkout>`. Fetch date and the git version are printed with every run.
* Binary-ish blobs: `z3fetch -group fdroid-apk` (five F-Droid client APKs, 12-13 MB each, already pinned in `sources.json`) re-exported
  as five commits of one path by `z3git binhist`. The bytes are real; the history is synthetic, so it says nothing about an independent Git producer.
