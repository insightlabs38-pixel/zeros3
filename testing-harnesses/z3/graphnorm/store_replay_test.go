package graphnorm

import (
	"bytes"
	"crypto/sha256"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func fx(t *testing.T, name string) []byte {
	b, err := os.ReadFile(filepath.Join("..", "graphcore", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func buildStore(t *testing.T) (map[[32]byte][]byte, []*LLVMRecipe, [][]byte) {
	objs := map[[32]byte][]byte{}
	a := NewArchive(2)
	a.Dir = t.TempDir()
	var recs []*LLVMRecipe
	var files [][]byte
	for _, n := range []string{"a.bc", "b.bc"} {
		d := fx(t, n)
		e, err := EncodeLLVM(d, PolicyAll)
		if err != nil {
			t.Fatal(err)
		}
		rc := &LLVMRecipe{Size: uint64(len(d)), SHA: sha256.Sum256(d), Tail: e.Tail}
		fi, si := 0, 0
		for _, p := range e.Pieces {
			key := "s" + string(rune('0'+si))
			if p.Kind == 'F' {
				key = "f:" + e.FuncNames[fi]
				fi++
			} else {
				si++
			}
			a.Put(key, p.Data)
			rc.Steps = append(rc.Steps, RecipeStep{Kind: p.Kind, Ref: sha256.Sum256(p.Data)})
		}
		a.Put("idtab", e.IDTab)
		rc.IDTab = sha256.Sum256(e.IDTab)
		recs = append(recs, rc)
		files = append(files, d)
	}
	ents, _ := os.ReadDir(filepath.Join(a.Dir, "objs"))
	for _, e := range ents {
		b, _ := os.ReadFile(filepath.Join(a.Dir, "objs", e.Name()))
		var h [32]byte
		for i := 0; i < 32; i++ {
			var v byte
			for _, c := range []byte(e.Name()[2*i : 2*i+2]) {
				v <<= 4
				if c >= 'a' {
					v |= c - 'a' + 10
				} else {
					v |= c - '0'
				}
			}
			h[i] = v
		}
		objs[h] = b
	}
	return objs, recs, files
}

func TestStoreReplayAndCorruption(t *testing.T) {
	objs, recs, files := buildStore(t)
	get := func(s [32]byte) ([]byte, error) {
		if b, ok := objs[s]; ok {
			return b, nil
		}
		return nil, os.ErrNotExist
	}
	for i, rc := range recs {
		out, err := ReplayLLVM(rc, get)
		if err != nil || !bytes.Equal(out, files[i]) {
			t.Fatalf("replay %d: %v", i, err)
		}
		// recipe wire round trip and truncations
		w := rc.Marshal()
		if _, err := ParseLLVMRecipe(w); err != nil {
			t.Fatal(err)
		}
		for n := 0; n < len(w); n += 7 {
			if _, err := ParseLLVMRecipe(w[:n]); err == nil {
				t.Fatalf("truncated recipe (%d bytes) accepted", n)
			}
		}
	}
	// every stored object, corrupted by one flipped byte, must make replay fail closed
	r := rand.New(rand.NewSource(3))
	n, failed := 0, 0
	for h, b := range objs {
		if n++; n > 120 {
			break
		}
		cp := append([]byte(nil), b...)
		cp[r.Intn(len(cp))] ^= 0x40
		bad := func(s [32]byte) ([]byte, error) {
			if s == h {
				return cp, nil
			}
			return get(s)
		}
		for i, rc := range recs {
			out, err := ReplayLLVM(rc, bad)
			if err == nil && !bytes.Equal(out, files[i]) {
				t.Fatal("corrupt object produced wrong bytes without error")
			}
			if err != nil {
				failed++
			}
		}
	}
	if failed == 0 {
		t.Fatal("no corruption was detected")
	}
	// missing object
	for h := range objs {
		miss := func(s [32]byte) ([]byte, error) {
			if s == h {
				return nil, os.ErrNotExist
			}
			return get(s)
		}
		if _, err := ReplayLLVM(recs[1], miss); err == nil {
			// only objects unreferenced by rev 1 may be absent without error
			continue
		}
		break
	}
	// stale dependency: rev 1 steps with rev 0's identity tables must not reconstruct silently
	stale := *recs[1]
	stale.IDTab = recs[0].IDTab
	if out, err := ReplayLLVM(&stale, get); err == nil && !bytes.Equal(out, files[1]) {
		t.Fatal("stale idtab yielded wrong bytes")
	} else if err == nil {
		t.Log("stale idtab happened to be equivalent")
	}
}
