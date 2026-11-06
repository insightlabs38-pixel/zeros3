package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"zeros3-z3/graphnorm"
)

// cmdStore builds a persistent hybrid store (templates + B1 deltas) for a history.
func cmdStore(dir string, pol graphnorm.Policy, files []string) {
	a := graphnorm.NewArchive(2)
	a.Dir = dir
	must(os.MkdirAll(filepath.Join(dir, "recipes"), 0o755))
	manifest := ""
	for i, f := range files {
		d, err := os.ReadFile(f)
		must(err)
		e, err := graphnorm.EncodeLLVM(d, pol)
		must(err)
		rc := &graphnorm.LLVMRecipe{Size: uint64(len(d)), SHA: sha256.Sum256(d), Tail: e.Tail}
		fi, si := 0, 0
		for _, p := range e.Pieces {
			key := fmt.Sprintf("s:%d", si)
			if p.Kind == 'F' {
				key = "f:" + e.FuncNames[fi]
				fi++
			} else {
				si++
			}
			a.Put(key, p.Data)
			rc.Steps = append(rc.Steps, graphnorm.RecipeStep{Kind: p.Kind, Ref: sha256.Sum256(p.Data)})
		}
		a.Put("idtab", e.IDTab)
		rc.IDTab = sha256.Sum256(e.IDTab)
		name := fmt.Sprintf("rev%02d", i)
		must(os.WriteFile(filepath.Join(dir, "recipes", name), rc.Marshal(), 0o644))
		manifest += fmt.Sprintf("%s %s %d %s\n", name, hex.EncodeToString(rc.SHA[:]), len(d), filepath.Base(f))
	}
	must(os.WriteFile(filepath.Join(dir, "MANIFEST"), []byte(manifest), 0o644))
	fmt.Printf("stored %d revisions, %d objects, charged %d bytes (+refs not included)\n", len(files), a.Objects, a.Total)
}

// cmdReplay is the fresh-process reader: it opens only recipes/ and objs/.
func cmdReplay(dir, out string) {
	must(os.MkdirAll(out, 0o755))
	ents, err := os.ReadDir(filepath.Join(dir, "recipes"))
	must(err)
	get := func(s [32]byte) ([]byte, error) {
		return os.ReadFile(filepath.Join(dir, "objs", hex.EncodeToString(s[:])))
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(dir, "recipes", e.Name()))
		must(err)
		rc, err := graphnorm.ParseLLVMRecipe(b)
		must(err)
		data, err := rc.Replay(get)
		must(err)
		must(os.WriteFile(filepath.Join(out, e.Name()), data, 0o644))
		fmt.Printf("%s %d bytes ok\n", e.Name(), len(data))
	}
}
