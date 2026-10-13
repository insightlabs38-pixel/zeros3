# Preflate oracle (opt-in, research only)

`preflate-rs` is an external oracle for Z3-04. It is not a Go dependency, is not
vendored, and is not needed to build or test ZeroS3 or the Z3 module; without it
the `preflate*` representations report "not applicable" and `TestPreflateRealOracle`
skips.

| | |
|---|---|
| repository | https://github.com/microsoft/preflate-rs |
| pinned commit | `3bcd33441849a8a25d2b26077128f4e3a88d071b` (tag `v0.7.6`) |
| license | Apache-2.0 (upstream `LICENSE`); crates pulled by cargo carry their own licenses |
| toolchain | stable Rust (built with rustc 1.97.0; upstream requires >= 1.89) |
| build | `cargo build --release` of `wrap/` (this directory's `Cargo.toml` + `src/main.rs`, path-depending on the pinned checkout) with the upstream `Cargo.lock` |
| API used | `preflate_whole_deflate_stream` / `recreate_whole_deflate_stream`; `max_chain_length` 4096, `plain_text_limit` 1 GiB, internal verify **off** (exactness is checked by the lab) |

```sh
testing-harnesses/z3/oracle/preflate/setup.sh     # clones into ${Z3_CACHE:-~/.cache/z3-oracle}, prints the binary path
export Z3_PREFLATE=<printed path>                 # optional: default cache path is probed automatically
```

`z3preflate analyze IN PLAIN RECON` writes the plaintext and Preflate's complete
reconstruction blob (parameters + CABAC corrections; the blob's size is the
"recon" cost). `z3preflate recreate PLAIN RECON OUT` rebuilds the stream. Each
prints one JSON line with tool-internal time in nanoseconds.
