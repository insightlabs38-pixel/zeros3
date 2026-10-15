package gitz3

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"

	"zeros3-z3/lab"
)

func fx(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var algos = []Algo{SHA1, SHA256}

// exact runs a plan through the family accounting and rebuilds it from the store.
func exact(t testing.TB, ac *lab.Acct, pl *lab.Plan, want []byte) lab.Account {
	t.Helper()
	acc, err := ac.Add(pl)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Rebuild == nil {
		t.Fatal("no rebuild")
	}
	var out bytes.Buffer
	if err := pl.Rebuild(&out, ac.Get); err != nil {
		t.Fatal(err)
	}
	if out.Len() != len(want) || sha256.Sum256(out.Bytes()) != sha256.Sum256(want) {
		t.Fatalf("rebuild differs: %d vs %d bytes", out.Len(), len(want))
	}
	return acc
}

func TestFixtureLoose(t *testing.T) {
	for _, a := range algos {
		for _, ty := range []string{"blob", "tree", "commit", "tag"} {
			b := fx(t, fmt.Sprintf("%s-loose-%s", a, ty))
			name := strings.TrimSpace(string(fx(t, fmt.Sprintf("%s-loose-%s.name", a, ty))))
			lo, err := ParseLoose(a, b, Limits{})
			if err != nil {
				t.Fatalf("%v %s: %v", a, ty, err)
			}
			if lo.Type.String() != ty {
				t.Fatalf("type %v", lo.Type)
			}
			h := HexID(lo.ID)
			if name != h[:2]+"/"+h[2:] {
				t.Fatalf("name %s vs id %s", name, h)
			}
			if err := lo.VerifyID(lo.ID); err != nil {
				t.Fatal(err)
			}
			// exact representation, all modes
			for _, mode := range []string{"zlib", "plain", "auto"} {
				ac := lab.NewAcct(96, true)
				pl, err := LoosePlan(a, LooseFile{Path: name, Data: b}, RepOpts{Mode: mode, Has: ac.Has})
				if err != nil {
					t.Fatal(err)
				}
				exact(t, ac, pl, b)
			}
			// a wrong name is carried in the recipe, still exact
			ac := lab.NewAcct(96, true)
			pl, _ := LoosePlan(a, LooseFile{Path: "zz/notthename", Data: b}, RepOpts{Mode: "plain"})
			exact(t, ac, pl, b)
		}
	}
}

func TestFixturePacks(t *testing.T) {
	for _, a := range algos {
		for _, v := range []string{"ofs", "ref"} {
			name := fmt.Sprintf("%s-%s", a, v)
			data, idx := fx(t, name+".pack"), fx(t, name+".idx")
			p, err := ParsePack(data, a, Limits{})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			st, err := p.Resolve()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if st.Delta == 0 {
				t.Fatalf("%s: fixture has no delta", name)
			}
			for i := range p.Entries {
				e := &p.Entries[i]
				pay, err := p.Object(i, nil)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(ObjectID(a, e.Type, pay), e.ID) {
					t.Fatalf("%s entry %d: native id mismatch", name, i)
				}
				if e.Kind == ofsDelta && v == "ref" || e.Kind == refDelta && v == "ofs" {
					t.Fatalf("%s: unexpected delta kind %d", name, e.Kind)
				}
			}
			x, err := ParseIdx(idx, a, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if err := x.CheckAgainstPack(p); err != nil {
				t.Fatal(err)
			}
			bi, err := BuildIdx(p)
			if err != nil || !bytes.Equal(bi, idx) {
				t.Fatalf("%s: idx not byte-derivable (%v)", name, err)
			}
			for i := range p.Entries {
				pos, _ := x.Lookup(p.Entries[i].ID)
				if pos < 0 || x.Offset(pos) != p.Entries[i].Off {
					t.Fatalf("lookup %d", i)
				}
			}
			if pos, _ := x.Lookup(bytes.Repeat([]byte{0xee}, a.Size())); pos != -1 {
				t.Fatal("found absent id")
			}
			// every exact representation reconstructs the pack and its index
			an, err := AnalyzePack(p)
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"zlib", "plain", "auto"} {
				for layout := 0; layout < 2; layout++ {
					ac := lab.NewAcct(96, true)
					o := RepOpts{Mode: mode, Has: ac.Has}
					var pl *lab.Plan
					if layout == 0 {
						pl, _ = an.SeqPlan(o)
					} else {
						pl, _ = an.SetPlan(o)
					}
					exact(t, ac, pl, data)
					ip, ok := IdxPlan(p, idx, pl)
					if !ok {
						t.Fatal("idx plan not derivable")
					}
					exact(t, ac, ip, idx)
				}
			}
			ac := lab.NewAcct(96, true)
			exact(t, ac, EntryWholePlan(p, RepOpts{}), data)
			if _, err := NativeObjPlan(p, RepOpts{}); err != nil {
				t.Fatal(err)
			}
		}
		// thin pack: parses, cannot resolve, falls back
		thin := fx(t, a.String()+"-thin.pack")
		p, err := ParsePack(thin, a, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Resolve(); err == nil || !strings.Contains(err.Error(), "unresolved") {
			t.Fatalf("thin pack resolve: %v", err)
		}
		l := NewLab(96, true)
		rows := l.RunStates([]*State{{Label: "thin", Tag: int(a), Files: []File{{Name: "pack/t.pack", Kind: "pack", Data: thin}}}}, ExactReps())
		for _, r := range rows {
			if !r.Oracle && !r.Exact {
				t.Fatalf("%s thin fallback not exact: %s", r.Rep, r.Err)
			}
		}
	}
}

func TestDeltaSemantics(t *testing.T) {
	base := []byte("hello world, hello git delta")
	// copy 5 bytes at 0, insert "!!", copy 4 at 12
	d := []byte{byte(len(base)), 11}
	d = append(d, 0x80|0x10, 5)
	d = append(d, 2, '!', '!')
	d = append(d, 0x80|0x01|0x10, 12, 4)
	out, err := ApplyDelta(base, d, 0)
	if err != nil || string(out) != "hello!! hel" {
		t.Fatalf("%q %v", out, err)
	}
	st, err := ScanDelta(d)
	if err != nil || st.Copies != 2 || st.Inserts != 1 || st.Tgt != 11 {
		t.Fatalf("%+v %v", st, err)
	}
	bad := map[string][]byte{
		"opcode0":       {byte(len(base)), 1, 0},
		"copy-outside":  {byte(len(base)), 5, 0x80 | 0x01 | 0x10, 200, 5},
		"short-insert":  {byte(len(base)), 5, 5, 'a'},
		"size-mismatch": {byte(len(base)), 9, 2, 'a', 'b'},
		"wrong-source":  {5, 2, 2, 'a', 'b'},
		"overflow":      {byte(len(base)), 3, 0x80 | 0x01 | 0x10, 10, 200},
		"truncated":     {byte(len(base))},
		"amplify":       {byte(len(base)), 0xff, 0xff, 0xff, 0xff, 0x7f, 0x80},
	}
	for n, b := range bad {
		if _, err := ApplyDelta(base, b, 0); err == nil {
			t.Errorf("%s: accepted", n)
		}
	}
	if _, err := ApplyDelta(base, d, 5); err == nil {
		t.Error("maxOut not enforced")
	}
}

func TestIdxV1AndLargeOffsets(t *testing.T) {
	for _, a := range algos {
		data, idx := fx(t, a.String()+"-v1.pack"), fx(t, a.String()+"-v1.idx")
		p, err := ParsePack(data, a, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Resolve(); err != nil {
			t.Fatal(err)
		}
		ids, offs, err := ParseIdxV1(idx, a, Limits{})
		if err != nil || len(ids) != len(p.Entries) {
			t.Fatalf("v1 parse: %v (%d ids)", err, len(ids))
		}
		for i, id := range ids {
			j := p.IndexOfID(id)
			if j < 0 || p.Entries[j].Off != int64(offs[i]) {
				t.Fatalf("v1 record %d disagrees with the pack", i)
			}
		}
		bi, err := BuildIdxV1(p)
		if err != nil || !bytes.Equal(bi, idx) {
			t.Fatalf("%v: v1 index not byte-derivable (%v)", a, err)
		}
		if _, err := ParseIdx(idx, a, Limits{}); err == nil {
			t.Fatal("v2 parser accepted a v1 index")
		}
		for n := 0; n < len(idx); n += 7 {
			if _, _, err := ParseIdxV1(idx[:n], a, Limits{}); err == nil {
				t.Fatalf("v1 truncation %d accepted", n)
			}
		}
		// 64-bit offsets: a synthetic pack whose second entry lies beyond 2 GiB
		sp := &Pack{Algo: a, Trailer: bytes.Repeat([]byte{7}, a.Size()), resolved: true, Entries: []Entry{
			{ID: bytes.Repeat([]byte{1}, a.Size()), Off: 12, CRC: 0xdeadbeef},
			{ID: bytes.Repeat([]byte{2}, a.Size()), Off: 3<<31 + 5, CRC: 0x1234},
		}}
		big, err := BuildIdx(sp)
		if err != nil {
			t.Fatal(err)
		}
		x, err := ParseIdx(big, a, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if x.Offset(0) != 12 || x.Offset(1) != 3<<31+5 || x.CRC(1) != 0x1234 {
			t.Fatalf("64-bit offsets: %d %d", x.Offset(0), x.Offset(1))
		}
		if _, err := BuildIdxV1(sp); err == nil {
			t.Fatal("v1 cannot hold a 64-bit offset")
		}
	}
}

// The Git recipes fit the Z3-15F shared shape without new fields: converting a plan
// to a srev.Revision and back reproduces the accounting and the exact rebuild.
func TestSrevAdapterFits(t *testing.T) {
	for _, a := range algos {
		data := fx(t, a.String()+"-ofs.pack")
		p, _ := ParsePack(data, a, Limits{})
		p.Resolve()
		an, _ := AnalyzePack(p)
		for _, layout := range []string{"seq", "set"} {
			ac1, ac2 := lab.NewAcct(96, true), lab.NewAcct(96, true)
			o := RepOpts{Mode: "plain"}
			var pl *lab.Plan
			if layout == "seq" {
				pl, _ = an.SeqPlan(o)
			} else {
				pl, _ = an.SetPlan(o)
			}
			rev := RevisionOf(pl)
			if len(rev.Roots) != 0 || len(rev.Bases) != 0 {
				t.Fatal("Roots/Bases used")
			}
			pl2 := lab.PlanFromRevision(rev)
			a1, err := ac1.Add(pl)
			if err != nil {
				t.Fatal(err)
			}
			a2, err := ac2.Add(pl2)
			if err != nil {
				t.Fatal(err)
			}
			if lab.TableCost(a1) != lab.TableCost(a2) || a1.NewBytes != a2.NewBytes {
				t.Errorf("%v %s: cost %d vs %d (new %d vs %d)", a, layout, lab.TableCost(a1), lab.TableCost(a2), a1.NewBytes, a2.NewBytes)
			}
			var out bytes.Buffer
			if err := pl2.Rebuild(&out, ac2.Get); err != nil || !bytes.Equal(out.Bytes(), data) {
				t.Errorf("%v %s: rev rebuild: %v", a, layout, err)
			}
			if layout == "set" && a == SHA256 {
				n := 0
				for _, m := range rev.Members {
					if len(m.C.Native) > 0 && string(m.C.Native) == string(m.C.SHA[:]) {
						n++
					}
				}
				if n == 0 {
					t.Error("no Native hint equals the component SHA in a SHA-256 repository")
				}
			}
		}
	}
}

func TestAdmit(t *testing.T) {
	for _, a := range algos {
		pk := fx(t, a.String()+"-ofs.pack")
		// with its index derivable the exact layouts are cheaper than CDC even on first ingest (negative premium);
		// without the index (pack alone) a 2 KB pack does not repay a recipe at a tiny rewrite probability
		withIdx, err := Admit(pk, fx(t, a.String()+"-ofs.idx"), a, 0.001)
		if err != nil || withIdx.Layout == "cdc" || withIdx.QStar >= 0 {
			t.Errorf("%v with idx: %+v %v", a, withIdx, err)
		}
		alone, err := Admit(pk, nil, a, 0.001)
		if err != nil || alone.Layout != "cdc" {
			t.Errorf("%v pack alone q=0.001: %+v %v", a, alone, err)
		}
		// anything that does not parse is CDC with a reason, never an error
		bad, err := Admit(pk[:len(pk)-3], nil, a, 1)
		if err != nil || bad.Layout != "cdc" || bad.Reason == "" {
			t.Errorf("truncated: %+v %v", bad, err)
		}
		thin, _ := Admit(fx(t, a.String()+"-thin.pack"), nil, a, 1)
		if thin.Layout != "cdc" || thin.Reason != "fallback: thin-or-unresolved-delta" {
			t.Errorf("thin: %+v", thin)
		}
	}
}
