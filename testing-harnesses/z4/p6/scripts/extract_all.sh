#!/bin/bash
# usage: extract_all.sh OUTDIR   (run from the repository root)
# Mechanical extraction of the B3 (semcore + st/ggufz3/torchz3) and B4 (graphcore) cores into a TEMPORARY copy of the
# production zeros3.go: type-aware reachability from the listed roots, collision-checked renaming, compile,
# original-vs-extracted differential vectors, token-level fidelity. Nothing is written into the repository.
set -euo pipefail
O=$(realpath -m "$1"); mkdir -p "$O"; Z=testing-harnesses/z3
go -C $Z build -o "$O/p6extract" ./cmd/p6extract
B3ROOTS='semcore.NewStore,semcore.NewMemBackend,semcore.DetectAt,semcore.NewPlanner,semcore.DefaultOptions,semcore.ParseRecipe,semcore.ParseRecord,semcore.InvertPerm,semcore.DiffPatches,semcore.DiffPatchesMax,semcore.CID,semcore.PayloadSHA,semcore.Op,semcore.IO,semcore.Recipe,semcore.Desc,semcore.Backend,semcore.BlobReader,semcore.ErrMissing,st.Build,st.KV,st.BuildTensor'
B4ROOTS='graphcore.EncodeLLVM,graphcore.Policy,graphcore.PolicyNone,graphcore.PolicyAll,graphcore.LLVMRecipe,graphcore.RecipeStep,graphcore.ParseLLVMRecipe,graphcore.EncodePDB,graphcore.EncodeDEX,graphcore.NewByteMachine,graphcore.Graph,graphcore.Node,graphcore.Edge,graphcore.Idents,graphcore.FlagOpaque,graphcore.Match,graphcore.MatchCompact,graphcore.DefaultMatch,graphcore.OpLit,graphcore.OpZero,graphcore.CGraph'
sha_before=$(sha256sum zeros3.go go.mod | sha256sum | cut -c1-16)
Z3ABS=$(realpath $Z)
( cd $Z && P6_Z3=$Z3ABS "$O/p6extract" -module . -modpath zeros3-z3 -pkgs semcore -roots "$B3ROOTS" -prod ../../zeros3.go -work "$O/b3" -prefix sc -vectors semcore/vectors.go.txt -rehearsal semcore/rehearsal.go.txt ) | tee "$O/extract_b3.txt"
( cd $Z && P6_Z3=$Z3ABS "$O/p6extract" -module . -modpath zeros3-z3 -pkgs graphcore -roots "$B4ROOTS" -prod ../../zeros3.go -work "$O/b4" -prefix gc -vectors graphcore/vectors.go.txt -rehearsal graphcore/rehearsal.go.txt ) | tee "$O/extract_b4.txt"
sha_after=$(sha256sum zeros3.go go.mod | sha256sum | cut -c1-16)
echo "production zeros3.go+go.mod digest before=$sha_before after=$sha_after" | tee "$O/production_unchanged.txt"
[ "$sha_before" = "$sha_after" ]
# new module requirements introduced by the integrations (must be none)
diff <(cat go.mod) "$O/b3/integration/go.mod" && diff <(cat go.mod) "$O/b4/integration/go.mod" && echo "go.mod unchanged in both integrations" | tee -a "$O/production_unchanged.txt"
