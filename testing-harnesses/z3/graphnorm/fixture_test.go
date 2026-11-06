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
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFixtureExactBothPolicies(t *testing.T) {
	for _, n := range []string{"a.bc", "b.bc"} {
		d := fx(t, n)
		for _, pol := range []Policy{PolicyAll, PolicyNone, PolicyFromBits(5)} {
			e, err := EncodeLLVM(d, pol)
			if err != nil {
				t.Fatalf("%s %v: %v", n, pol, err)
			}
			if e.Fallback != 0 {
				t.Fatalf("%s: unexpected literal fallback %d", n, e.Fallback)
			}
			out, err := e.Decode()
			if err != nil || !bytes.Equal(out, d) {
				t.Fatalf("%s: decode mismatch %v", n, err)
			}
		}
	}
}

func pieceSet(e *LLVMEncoding) map[[32]byte]string {
	m := map[[32]byte]string{}
	i := 0
	for _, p := range e.Pieces {
		if p.Kind == 'F' {
			m[sha256.Sum256(p.Data)] = e.FuncNames[i]
			i++
		}
	}
	return m
}

// Inserting a type, a global and a function renumbers the module; unchanged functions must
// keep identical templates under normalization and must NOT under literal templates.
func TestRenumberingSharesTemplates(t *testing.T) {
	a, b := fx(t, "a.bc"), fx(t, "b.bc")
	count := func(pol Policy) int {
		ea, err := EncodeLLVM(a, pol)
		if err != nil {
			t.Fatal(err)
		}
		eb, err := EncodeLLVM(b, pol)
		if err != nil {
			t.Fatal(err)
		}
		pa, pb := pieceSet(ea), pieceSet(eb)
		n := 0
		for h := range pa {
			if _, ok := pb[h]; ok {
				n++
			}
		}
		return n
	}
	strong, none := count(PolicyAll), count(PolicyNone)
	t.Logf("shared function templates: strong=%d none=%d", strong, none)
	if strong <= none || strong < 2 {
		t.Fatalf("normalization did not increase sharing: strong=%d none=%d", strong, none)
	}
}

func TestBitFlipsNeverProduceWrongBytes(t *testing.T) {
	d := fx(t, "a.bc")
	r := rand.New(rand.NewSource(7))
	ok, rejected := 0, 0
	for i := 0; i < len(d)*2; i++ {
		x := append([]byte(nil), d...)
		x[r.Intn(len(x))] ^= byte(1 << r.Intn(8))
		e, err := EncodeLLVM(x, PolicyAll)
		if err != nil {
			rejected++
			continue
		}
		out, err := e.Decode()
		if err != nil || !bytes.Equal(out, x) {
			t.Fatalf("accepted corrupted input but reconstructed different bytes (err=%v)", err)
		}
		ok++
	}
	t.Logf("flips: %d accepted-and-exact, %d rejected", ok, rejected)
}

func TestTruncationsRejectedOrExact(t *testing.T) {
	d := fx(t, "b.bc")
	for n := 0; n < len(d); n += 4 {
		e, err := EncodeLLVM(d[:n], PolicyAll)
		if err != nil {
			continue
		}
		out, err := e.Decode()
		if err != nil || !bytes.Equal(out, d[:n]) {
			t.Fatalf("truncation %d accepted but inexact", n)
		}
	}
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
			rc.Steps = append(rc.Steps, RecipeStep{p.Kind, sha256.Sum256(p.Data)})
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
		out, err := rc.Replay(get)
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
			out, err := rc.Replay(bad)
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
		if _, err := recs[1].Replay(miss); err == nil {
			// only objects unreferenced by rev 1 may be absent without error
			continue
		}
		break
	}
	// stale dependency: rev 1 steps with rev 0's identity tables must not reconstruct silently
	stale := *recs[1]
	stale.IDTab = recs[0].IDTab
	if out, err := stale.Replay(get); err == nil && !bytes.Equal(out, files[1]) {
		t.Fatal("stale idtab yielded wrong bytes")
	} else if err == nil {
		t.Log("stale idtab happened to be equivalent")
	}
}

func TestRandomBitstreamRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	for trial := 0; trial < 200; trial++ {
		bc := &Bitcode{}
		info := map[uint64][]Abbrev{}
		for k := 0; k < 1+r.Intn(3); k++ {
			bc.Blocks = append(bc.Blocks, randBlock(r, 0, info, uint64(8+k)))
		}
		raw := bc.Bytes()
		got, err := ParseBitstream(raw)
		if err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
		if !bytes.Equal(got.Bytes(), raw) {
			t.Fatalf("trial %d: re-encode differs", trial)
		}
	}
}

func randBlock(r *rand.Rand, depth int, info map[uint64][]Abbrev, id uint64) *Block {
	b := &Block{ID: id, Width: uint64(3 + r.Intn(4))}
	var abbrevs []Abbrev
	abbrevs = append(abbrevs, info[id]...)
	for i := 0; i < 1+r.Intn(12); i++ {
		switch c := r.Intn(10); {
		case c == 0 && depth < 3:
			b.Items = append(b.Items, Item{Kind: ItemBlock, Blk: randBlock(r, depth+1, info, id+uint64(1+r.Intn(5)))})
		case c == 1:
			a := Abbrev{{Lit: true, Val: uint64(r.Intn(40))}, {Enc: encFixed, Width: uint64(1 + r.Intn(12))}, {Enc: encVBR, Width: uint64(2 + r.Intn(6))}}
			if r.Intn(2) == 0 {
				a = append(a, AbbrevOp{Enc: encArray}, AbbrevOp{Enc: encFixed, Width: 8})
			}
			b.Items = append(b.Items, Item{Kind: ItemDef, Def: a})
			abbrevs = append(abbrevs, a)
		default:
			rec := &Record{AbbrevID: 3, Code: uint64(1 + r.Intn(30))}
			for j := 0; j < r.Intn(6); j++ {
				rec.Ops = append(rec.Ops, uint64(r.Int63n(1<<uint(1+r.Intn(40)))))
			}
			if len(abbrevs) > 0 && r.Intn(2) == 0 {
				ai := r.Intn(len(abbrevs))
				a := abbrevs[ai]
				rec = &Record{AbbrevID: uint64(4 + ai), Code: a[0].Val}
				rec.Ops = append(rec.Ops, uint64(r.Intn(1<<(a[1].Width))))
				mask := uint64(1)<<(5*(a[2].Width-1)) - 1
				rec.Ops = append(rec.Ops, uint64(r.Int63())&mask)
				if len(a) > 3 {
					for j := 0; j < r.Intn(5); j++ {
						rec.Ops = append(rec.Ops, uint64(r.Intn(256)))
					}
				}
			}
			b.Items = append(b.Items, Item{Kind: ItemRec, Rec: rec})
		}
	}
	return b
}

func TestPDBAndDEXFixtures(t *testing.T) {
	pdb, err := os.ReadFile("../debugsym/testdata/portable-small.pdb")
	if err != nil {
		t.Fatal(err)
	}
	for _, search := range []bool{false, true} {
		e, err := EncodePDB(pdb, search)
		if err != nil {
			t.Fatal(err)
		}
		out, err := e.Decode()
		if err != nil || !bytes.Equal(out, pdb) {
			t.Fatalf("pdb decode: %v", err)
		}
	}
	for _, f := range []string{"fixture-api24.dex", "fixture-api28.dex"} {
		d, err := os.ReadFile("../dexz3/testdata/" + f)
		if err != nil {
			t.Fatal(err)
		}
		e, err := EncodeDEX(d, true)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		m := NewByteMachine()
		m.Blobs = e.Blobs
		if err := m.Exec(e.Program); err != nil || !bytes.Equal(m.Output(), d) {
			t.Fatalf("%s: replay %v", f, err)
		}
	}
	// mutation: every accepted PDB mutation must still reconstruct exactly
	r := rand.New(rand.NewSource(5))
	for i := 0; i < 2000; i++ {
		x := append([]byte(nil), pdb...)
		for k := 0; k < 1+r.Intn(3); k++ {
			x[r.Intn(len(x))] = byte(r.Intn(256))
		}
		e, err := EncodePDB(x, true)
		if err != nil {
			continue
		}
		if out, err := e.Decode(); err != nil || !bytes.Equal(out, x) {
			t.Fatal("accepted mutated PDB reconstructs differently")
		}
	}
}
