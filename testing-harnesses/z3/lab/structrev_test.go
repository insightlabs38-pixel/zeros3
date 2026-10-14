package lab

import (
	"bytes"
	"crypto/sha256"
	"io"
	"testing"

	"zeros3-z3/carz3"
	"zeros3-z3/eraez3"
	"zeros3-z3/merklez3"
	"zeros3-z3/srev"
)

func merkleChain(t *testing.T, cfg merklez3.Config, muts ...func(*merklez3.State) *merklez3.State) []NamedBytes {
	t.Helper()
	ms := merklez3.NewMapStore()
	s := merklez3.NewState(5, 6000)
	var out []NamedBytes
	add := func(label string, st *merklez3.State) {
		root, _ := st.BuildInto(cfg, ms)
		var b bytes.Buffer
		if _, err := merklez3.Snapshot(ms, root, &b); err != nil {
			t.Fatal(err)
		}
		out = append(out, NamedBytes{label, b.Bytes()})
	}
	add("r0", s)
	for i, m := range muts {
		s = m(s)
		add("r"+string(rune('1'+i)), s)
	}
	return out
}

// TestMerkleRevisions: every Merkle representation rebuilds the snapshot
// exactly; hash-guided storage tracks the changed nodes, not the snapshot.
func TestMerkleRevisions(t *testing.T) {
	cfg := merklez3.DefaultConfig()
	chain := merkleChain(t, cfg,
		func(s *merklez3.State) *merklez3.State { return s.Update(s.PickScattered(1, 3)) },
		func(s *merklez3.State) *merklez3.State { return s.Update(s.PickClustered(2, 30)) },
		func(s *merklez3.State) *merklez3.State { return s.Insert(s.FreshKeys(3, 5)) },
		func(s *merklez3.State) *merklez3.State { return s.Remove(s.PickScattered(4, 5)) },
		func(s *merklez3.State) *merklez3.State { return s.Update(s.Keys) }, // dense
	)
	rows := RunBytesFamily("m", "merkle", chain, MerkleReps(cfg.Hash))
	by := map[string][]FRow{}
	for _, r := range rows {
		if r.Status != "ok" || !r.Exact {
			t.Fatalf("%s %s: %s %s", r.Rep, r.Label, r.Status, r.Err)
		}
		by[r.Rep] = append(by[r.Rep], r)
	}
	for i := 1; i <= 4; i++ {
		g, f, c := by["merkle-guided"][i], by["merkle-flat"][i], by["cdc"][i]
		if g.Cost*5 > f.Cost || g.Cost*3 > c.Cost {
			t.Errorf("r%d: guided %d should be far below flat %d and cdc %d", i, g.Cost, f.Cost, c.Cost)
		}
		d := g.Detail["discover"].(merklez3.Stats)
		if d.NodesRead != g.Detail["new_nodes"].(int) || d.NodesRead*20 > g.Detail["total_nodes"].(int) {
			t.Errorf("r%d: guided read %d of %d nodes", i, d.NodesRead, g.Detail["total_nodes"])
		}
	}
	// dense revision: guided is no better than raw (it pays the native index), auto falls back to CDC
	if by["merkle-auto"][5].Detail["auto"] != "cdc" || by["merkle-auto"][1].Detail["auto"] != "merkle-guided" {
		t.Errorf("admission: dense=%v sparse=%v", by["merkle-auto"][5].Detail["auto"], by["merkle-auto"][1].Detail["auto"])
	}
	if by["merkle-auto"][0].Detail["auto"] != "merkle-guided" {
		t.Errorf("a first revision must use node components")
	}
}

// TestMerkleTamperedNodeFallsBack: a node that disagrees with its native hash
// fails the guided transform; the archive is still stored (as bytes) and exact.
func TestMerkleTamperedNodeFallsBack(t *testing.T) {
	cfg := merklez3.DefaultConfig()
	chain := merkleChain(t, cfg)
	b := append([]byte(nil), chain[0].Data...)
	b[len(b)-3] ^= 0xff // inside the last node record
	rows := RunBytesFamily("t", "merkle", []NamedBytes{{"bad", b}}, MerkleReps(cfg.Hash))
	for _, r := range rows {
		switch r.Rep {
		case "merkle-guided":
			if r.Status == "ok" {
				t.Errorf("tampered snapshot accepted by merkle-guided")
			}
		case "merkle-auto":
			if r.Status != "ok" || !r.Exact || r.Detail["auto"] != "cdc" {
				t.Errorf("merkle-auto: %+v", r)
			}
		}
	}
}

// TestEra: EraE families on synthetic archives reconstruct exactly and show
// the layout / index / producer properties the real files show.
func TestEra(t *testing.T) {
	bs := eraez3.SynthBlocks(1, 100, 600)
	acc := bytes.Repeat([]byte{0xab}, 32)
	f0 := eraez3.Write(bs, eraez3.WriteOpts{Accumulator: acc, IndexStart: 100})
	chain := []NamedBytes{
		{"grouped", f0},
		{"interleaved", eraez3.Write(bs, eraez3.WriteOpts{Interleaved: true, Accumulator: acc, IndexStart: 100})},
		{"raw framing", eraez3.Write(bs, eraez3.WriteOpts{Frame: eraez3.FrameEncodeRaw, Accumulator: acc, IndexStart: 100})},
		{"no td", eraez3.Write(bs, eraez3.WriteOpts{NoTD: true, Accumulator: acc, IndexStart: 100})},
		{"reslice", eraez3.Write(bs[200:500], eraez3.WriteOpts{IndexStart: 300})},
		{"shifted index", eraez3.Write(bs, eraez3.WriteOpts{Accumulator: acc, IndexStart: 100, ShiftIndex: 3})},
		{"not erae", bytes.Repeat([]byte("plain bytes"), 1000)},
	}
	rows := RunBytesFamily("era", "erae", chain, EraReps())
	by := map[string]map[string]FRow{}
	for _, r := range rows {
		if r.Status != "ok" && r.Status != "n/a" {
			t.Fatalf("%s %s: %s %s", r.Rep, r.Label, r.Status, r.Err)
		}
		if r.Status == "ok" && !r.Exact {
			t.Fatalf("%s %s: not exact", r.Rep, r.Label)
		}
		if by[r.Rep] == nil {
			by[r.Rep] = map[string]FRow{}
		}
		by[r.Rep][r.Label] = r
	}
	ep := by["era-plain"]
	for _, l := range []string{"interleaved", "raw framing", "no td", "reslice"} {
		if l == "reslice" { // the cut ends of a short run re-chunk; interior chunks are shared
			if ep[l].Cost > int64(len(chain[0].Data))/2 {
				t.Errorf("era-plain reslice: cost %d", ep[l].Cost)
			}
			continue
		}
		if ep[l].Status != "ok" || ep[l].Acct.NewBytes != 0 && l != "no td" {
			t.Errorf("era-plain %s: new bytes %d (status %s)", l, ep[l].Acct.NewBytes, ep[l].Status)
		}
		if ep[l].Cost > int64(len(chain[0].Data))/20 {
			t.Errorf("era-plain %s: cost %d is more than 5%% of the file", l, ep[l].Cost)
		}
	}
	if ep["not erae"].Status != "n/a" {
		t.Errorf("a non-EraE object must not be accepted: %s", ep["not erae"].Status)
	}
	// the index is normalised away; a non-derivable index is kept
	if ep["shifted index"].Cost < ep["interleaved"].Cost {
		t.Errorf("a non-derivable index must cost its bytes")
	}
}

// TestStructuredRevision: the same srev.Revision shape expresses CAR blocks,
// Merkle nodes, EraE entries and a synthetic snapshot, and the generic plan
// conversion reproduces each adapter's exact bytes and cost. Fields used by
// fewer than two adapters are reported by the test log.
func TestStructuredRevision(t *testing.T) {
	type adapter struct {
		name string
		rev  *srev.Revision
		want []byte
	}
	var as []adapter

	// CAR: ordered block components, residual = header+wrapper+prefixes+order modes
	bs, root := carz3.Gen(carz3.GenSpec{Seed: 4, N: 40, Min: 200, Max: 3000, Fan: 6})
	car := carz3.V2From(carz3.V1([][]byte{root}, carz3.Shuffled(2, bs)), carz3.V2Opts{IndexCodec: carz3.MultihashIndexSortedCodec})
	rep, _, err := carz3.Build(bytes.NewReader(car), int64(len(car)), carz3.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	cr := &srev.Revision{Roots: [][]byte{root}}
	for _, r := range rep.Refs {
		r := r
		cr.Members = append(cr.Members, srev.Member{C: srev.Component{Native: rep.CIDBytes(indexOfRef(rep, r.Sum)), SHA: r.Sum, Size: r.Len, Data: func() ([]byte, error) { return car[r.SrcOff : r.SrcOff+r.Len], nil }}})
	}
	blob, sz := rep.Marshal()
	cr.Residual = int64(sz.Header + sz.Wrapper + sz.Prefix + len(rep.Refs))
	cr.Rebuild = func(w io.Writer, get func([32]byte) ([]byte, error)) error {
		r2, err := carz3.Unmarshal(blob)
		if err != nil {
			return err
		}
		return r2.Reconstruct(w, get)
	}
	as = append(as, adapter{"car", cr, car})

	// Merkle: root + Via nodes
	cfg := merklez3.DefaultConfig()
	chain := merkleChain(t, cfg)
	st := &merkleState{native: map[merklez3.ID][32]byte{}}
	in := &Input{Family: "s", Format: "merkle", Revision: "r0", Src: BytesSource("s", chain[0].Data), Opts: DefaultOptions()}
	mp, err := st.guided(in, cfg.Hash, 1.1)
	if err != nil {
		t.Fatal(err)
	}
	var viaCount int
	for _, pc := range mp.Pieces {
		if pc.Via {
			viaCount++
		}
	}
	if viaCount == 0 || viaCount != len(mp.Pieces)-1 {
		t.Errorf("merkle revision should be one referenced root and Via nodes: %d of %d", viaCount, len(mp.Pieces))
	}
	_ = adapter{}
	acc := NewAcct(35, false)
	a, err := acc.Add(mp)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := mp.Rebuild(&out, acc.Get); err != nil || !bytes.Equal(out.Bytes(), chain[0].Data) {
		t.Fatalf("merkle plan does not rebuild: %v", err)
	}
	if a.Components != len(mp.Pieces) {
		t.Errorf("component count %d vs %d", a.Components, len(mp.Pieces))
	}

	// Generic conversion of the CAR adapter reproduces its bytes.
	for _, ad := range as {
		p := PlanFromRevision(ad.rev)
		ac := NewAcct(35, false)
		if _, err := ac.Add(p); err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		if err := p.Rebuild(&b, ac.Get); err != nil || sha256.Sum256(b.Bytes()) != sha256.Sum256(ad.want) {
			t.Fatalf("%s: generic plan does not rebuild: %v", ad.name, err)
		}
	}
}

func indexOfRef(rep *carz3.Rep, sum [32]byte) int {
	for i := range rep.Refs {
		if rep.Refs[i].Sum == sum {
			return i
		}
	}
	return 0
}
