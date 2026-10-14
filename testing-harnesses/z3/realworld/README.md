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
