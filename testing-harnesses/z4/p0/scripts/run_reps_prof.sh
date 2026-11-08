#!/bin/bash
# CPU/alloc/block profiles of representation creation (shadow) and prefer-mode reads, one rep per kind.
. "$(dirname "$0")/lib.sh"
C=$(realpath "${1:?corpus}"); F=reps_prof.jsonl; mkdir -p "$P0_RES/prof"
sum() { for d in "$P0_WORK"/prof/$1-r*; do [ -d "$d" ] && "$(dirname "$0")/profsum.sh" "$d" "$P0_RES/prof/$(basename "$d").txt"; done; }
PR() { l=$1; shift; putrun $l $F -prof full -reps 1 -conc 1 -sargs "-representations shadow" "$@"; sum $l; }
GR() { l=$1; shift; getrun $l $F -prof full -reps 1 -conc 1 -sargs "-representations prefer" "$@"; sum $l; }
PR rp_tar     -files $C/src/coreutils-9.5.tar,$C/src/coreutils-9.6.tar,$C/src/coreutils-9.7.tar
PR rp_sqlite  -files $C/sqlite/v0.db,$C/sqlite/v1.db
PR rp_git     -files $C/git/h0.pack,$C/git/h1.pack
PR rp_merkle  -files $C/merkle/state-0.snap
PR rp_gzip    -files $C/src/coreutils-9.5.tar.gz
PR rp_zstd    -files $C/zstd/coreutils-9.7.l3.tar.zst
PR rp_model   -files $C/models/pythia14m/step1000.safetensors,$C/models/pythia14m/step2000.safetensors
PR rp_sst     -files $C/sst/zstd/c0.sst,$C/sst/zstd/c1.sst
PR rp_parquet -files $C/parquet/zstd/c0.parquet,$C/parquet/zstd/c1.parquet
PR rp_dex     -files $(ls $C/q6/fdroid-apk/*.dex | tr '\n' ',' | sed 's/,$//')
PR rp_clr     -files $(ls $C/q6/clr/*.dll | tr '\n' ',' | sed 's/,$//')
GR rp_get_tar_prefer_r4k -files $C/src/coreutils-9.7.tar -mode range -rsize 4kib -nreq 1500 -cache warm
GR rp_get_clr_prefer_whole -files $(ls $C/q6/clr/*.dll | tr '\n' ',' | sed 's/,$//') -mode whole -cache warm -nreq 40
echo REPS_PROF_DONE
