package lab

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"zeros3-z3/corpus"
)

type memSink struct {
	m    []corpus.Member
	data map[string][]byte
}

func (s *memSink) Put(m corpus.Member, d []byte) error {
	s.m = append(s.m, m)
	s.data[m.Path] = d
	return nil
}

type revision struct {
	corpus.Member
	data []byte
	src  Source
}

// genFamilies returns revisions of the named families at 64 KiB scale.
func genFamilies(t *testing.T, fams ...string) map[string][]revision {
	t.Helper()
	cfg := corpus.DefaultConfig()
	cfg.SizeKiB = 64
	cfg.Families = fams
	s := &memSink{data: map[string][]byte{}}
	if _, err := corpus.Generate(cfg, s); err != nil {
		t.Fatal(err)
	}
	out := map[string][]revision{}
	for _, m := range s.m {
		d := s.data[m.Path]
		out[m.Family] = append(out[m.Family], revision{m, d, BytesSource(m.Path, d)})
	}
	return out
}

func segMap(t *testing.T, format string, r revision) map[string]Seg {
	t.Helper()
	segs, err := Segment(format, r.src)
	if err != nil {
		t.Fatalf("%s %s: %v", r.Family, r.Revision, err)
	}
	m := map[string]Seg{}
	for _, g := range segs {
		if _, dup := m[g.Key]; dup {
			t.Fatalf("%s: duplicate segment key %s", r.Family, g.Key)
		}
		m[g.Key] = g
	}
	return m
}

func segBytes(r revision, g Seg) []byte { return r.data[g.Off : g.Off+g.Len] }

func TestSegmentersAgreeWithGroundTruth(t *testing.T) {
	g := genFamilies(t, "zip-stored-change", "zip-deflate-add", "st-modify-sparse", "st-reorder-data", "st-rename",
		"car-add", "car-reorder", "torch-update-some", "bin-rebase", "pagedb-update", "colstore-add-col", "ndarray-append", "protorec-insert-delete", "gguf-modify-tensor")

	// ZIP: one segment per member; unchanged members are byte-identical across
	// revisions even though the archive offsets move (add) or not (change).
	for _, fam := range []string{"zip-stored-change", "zip-deflate-add", "torch-update-some"} {
		format := g[fam][0].Format
		for i := 1; i < len(g[fam]); i++ {
			prev, cur := segMap(t, format, g[fam][i-1]), segMap(t, format, g[fam][i])
			tr := g[fam][i].Truth
			if len(cur) != len(tr["unchanged"])+len(tr["changed"])+len(tr["added"]) {
				t.Errorf("%s %s: %d segments, truth %v", fam, g[fam][i].Revision, len(cur), tr)
			}
			for _, name := range tr["unchanged"] {
				if !bytes.Equal(segBytes(g[fam][i], cur[name]), segBytes(g[fam][i-1], prev[name])) {
					t.Errorf("%s %s: unchanged member %s has different stored bytes", fam, g[fam][i].Revision, name)
				}
			}
			for _, name := range tr["changed"] {
				if bytes.Equal(segBytes(g[fam][i], cur[name]), segBytes(g[fam][i-1], prev[name])) {
					t.Errorf("%s %s: changed member %s has identical stored bytes", fam, g[fam][i].Revision, name)
				}
			}
		}
	}

	// Safetensors: tensors identified by name; layout changes never change tensor bytes.
	for _, fam := range []string{"st-modify-sparse", "st-reorder-data", "st-rename"} {
		for i := 1; i < len(g[fam]); i++ {
			prev, cur := segMap(t, "safetensors", g[fam][i-1]), segMap(t, "safetensors", g[fam][i])
			tr := g[fam][i].Truth
			for _, name := range tr["unchanged"] {
				if !bytes.Equal(segBytes(g[fam][i], cur[name]), segBytes(g[fam][i-1], prev[name])) {
					t.Errorf("%s %s: tensor %s differs", fam, g[fam][i].Revision, name)
				}
			}
			var covered int64
			for _, s := range cur {
				covered += s.Len
			}
			hl := int64(binary.LittleEndian.Uint64(g[fam][i].data))
			if covered != int64(len(g[fam][i].data))-8-hl {
				t.Errorf("%s: tensors cover %d, data section is %d", fam, covered, int64(len(g[fam][i].data))-8-hl)
			}
		}
	}
	if sm := segMap(t, "safetensors", g["st-reorder-data"][1]); sm["lm_head.weight"].Off == segMap(t, "safetensors", g["st-reorder-data"][0])["lm_head.weight"].Off {
		t.Error("reorder-data left lm_head at the same offset; test would be vacuous")
	}

	// CAR: one segment per block, keyed by CID; sets equal across a pure reorder.
	for i := 1; i < len(g["car-reorder"]); i++ {
		a, b := segMap(t, "car", g["car-reorder"][i-1]), segMap(t, "car", g["car-reorder"][i])
		if len(a) != len(b) || len(b) < 8 {
			t.Fatalf("car-reorder: %d vs %d blocks", len(a), len(b))
		}
		for k, s := range b {
			o, ok := a[k]
			if !ok || !bytes.Equal(segBytes(g["car-reorder"][i], s), segBytes(g["car-reorder"][i-1], o)) {
				t.Fatalf("block %.12s not identical across reorder", k)
			}
		}
		if fmt.Sprint(g["car-reorder"][i].Truth["blocks_total"]) != fmt.Sprint([]string{fmt.Sprint(len(b))}) {
			t.Errorf("segment count %d, truth %v", len(b), g["car-reorder"][i].Truth["blocks_total"])
		}
	}

	if n := len(segMap(t, "zbin", g["bin-rebase"][0])); n != 4 {
		t.Errorf("zbin sections: %d", n)
	}
	if n := len(segMap(t, "pagedb", g["pagedb-update"][0])); int64(n)*4096 != g["pagedb-update"][0].Size {
		t.Errorf("pagedb pages: %d", n)
	}
	if n, m := len(segMap(t, "colstore", g["colstore-add-col"][0])), len(segMap(t, "colstore", g["colstore-add-col"][1])); m <= n {
		t.Errorf("colstore-add-col chunks %d -> %d", n, m)
	}
	if n, m := len(segMap(t, "ndarray", g["ndarray-append"][0])), len(segMap(t, "ndarray", g["ndarray-append"][1])); m != n+2 {
		t.Errorf("ndarray-append chunks %d -> %d", n, m)
	}
	tr := g["protorec-insert-delete"][1].Truth
	if n := len(segMap(t, "protorec", g["protorec-insert-delete"][1])); n != len(tr["unchanged"])+len(tr["changed"])+len(tr["added"]) {
		t.Errorf("protorec records: %d, truth %v", n, tr)
	}
	if n := len(segMap(t, "gguf", g["gguf-modify-tensor"][0])); n != 12 {
		t.Errorf("gguf tensors: %d", n)
	}
	// Logical content of deflated members round-trips through the segmenter.
	for _, s := range segMap(t, "zip", g["zip-deflate-add"][0]) {
		if b, err := s.Logical(); err != nil || len(b) == 0 {
			t.Fatalf("logical content: %v", err)
		}
	}
}

func TestSegmentersNeverPanicOrEscapeBounds(t *testing.T) {
	g := genFamilies(t, "zip-stored-change", "st-modify-sparse", "gguf-requant", "car-add", "bin-rebase", "pagedb-update", "colstore-update-col", "ndarray-append", "protorec-update", "torch-update-some")
	rng := corpus.NewRNG(99, "seg-fuzz")
	for fam, revs := range g {
		r := revs[0]
		check := func(label string, data []byte) {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("%s %s: panic %v", fam, label, p)
				}
			}()
			segs, err := Segment(r.Format, BytesSource("x", data))
			if err != nil {
				return
			}
			end := int64(0)
			for _, s := range segs {
				if s.Off < end || s.Len <= 0 || s.Off+s.Len > int64(len(data)) {
					t.Fatalf("%s %s: segment %q [%d,+%d) escapes %d bytes", fam, label, s.Key, s.Off, s.Len, len(data))
				}
				end = s.Off + s.Len
			}
		}
		for _, n := range []int{0, 1, 7, 8, 31, 32, 100, len(r.data) / 2, len(r.data) - 1} {
			check(fmt.Sprintf("truncate %d", n), r.data[:min(n, len(r.data))])
		}
		for k := 0; k < 120; k++ {
			d := append([]byte(nil), r.data...)
			for j := 0; j < 1+rng.Intn(4); j++ {
				// corrupt headers/footers more often than payload
				pos := rng.Intn(min(len(d), 600))
				if k%3 == 0 {
					pos = len(d) - 1 - rng.Intn(min(len(d), 600))
				}
				d[pos] = byte(rng.U64())
			}
			check(fmt.Sprintf("flip %d", k), d)
		}
	}
}

func TestSegmentersRejectOverflowingOffsets(t *testing.T) {
	// safetensors header claiming tensors beyond the file, and a huge header length
	hdr := `{"a":{"dtype":"F32","shape":[1],"data_offsets":[0,18446744073709551615]}}`
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(len(hdr)))
	b = append(append(b, hdr...), 1, 2, 3, 4)
	if _, err := Segment("safetensors", BytesSource("x", b)); err == nil {
		t.Error("safetensors tensor beyond EOF accepted")
	}
	binary.LittleEndian.PutUint64(b, 1<<63)
	if _, err := Segment("safetensors", BytesSource("x", b)); err == nil {
		t.Error("safetensors absurd header length accepted")
	}
	hdr = `{"a":{"dtype":"F32","shape":[1],"data_offsets":[3,1]}}`
	b = make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(len(hdr)))
	if _, err := Segment("safetensors", BytesSource("x", append(append(b, hdr...), 0, 0, 0, 0))); err == nil {
		t.Error("safetensors inverted offsets accepted")
	}
	// overlapping tensors
	hdr = `{"a":{"dtype":"F32","shape":[1],"data_offsets":[0,4]},"b":{"dtype":"F32","shape":[1],"data_offsets":[2,6]}}`
	b = make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(len(hdr)))
	if _, err := Segment("safetensors", BytesSource("x", append(append(b, hdr...), make([]byte, 6)...))); err == nil {
		t.Error("overlapping tensors accepted")
	}
	// colstore footer entry with off+len wrapping uint64
	var buf bytes.Buffer
	buf.WriteString("ZCL1")
	foot := make([]byte, 4+24)
	binary.LittleEndian.PutUint32(foot, 1)
	binary.LittleEndian.PutUint64(foot[12:], ^uint64(0)-2)
	binary.LittleEndian.PutUint64(foot[20:], 10)
	buf.Write(foot)
	binary.Write(&buf, binary.LittleEndian, uint32(len(foot)))
	buf.WriteString("ZCL1")
	if _, err := Segment("colstore", BytesSource("x", buf.Bytes())); err == nil {
		t.Error("wrapping colstore span accepted")
	}
	if _, err := Segment("nope", BytesSource("x", nil)); err != ErrNoSegmenter {
		t.Errorf("unknown format: %v", err)
	}
	if _, err := Segment("gzip", BytesSource("x", nil)); err != ErrNoSegmenter {
		t.Errorf("gzip has no segmenter: %v", err)
	}
}

func accountPlans(t *testing.T, rep Rep, revs []revision, ref int64) []Account {
	t.Helper()
	ac := NewAcct(ref, false)
	var out []Account
	var prev *Input
	for _, r := range revs {
		in := &Input{Family: r.Family, Format: r.Format, Revision: r.Revision, Src: r.src, Prev: prev, Opts: DefaultOptions()}
		p, err := rep.Analyze(in)
		if err != nil {
			t.Fatalf("%s %s %s: %v", r.Family, r.Revision, rep.Name, err)
		}
		a, err := ac.Add(p)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if p.Rebuild != nil {
			if err := p.Rebuild(&buf, ac.Get); err != nil || !bytes.Equal(buf.Bytes(), r.data) {
				t.Fatalf("%s %s %s: reconstruction not exact (%v)", r.Family, r.Revision, rep.Name, err)
			}
		}
		out = append(out, a)
		prev = in
	}
	return out
}

func repByName(name string) Rep {
	for _, r := range Reps() {
		if r.Name == name {
			return r
		}
	}
	panic(name)
}

func TestSegmentReuseIsOffsetIndependent(t *testing.T) {
	g := genFamilies(t, "st-reorder-data", "st-reorder-meta", "car-reorder", "zip-stored-add")
	seg := repByName("struct-seg")
	for _, fam := range []string{"st-reorder-data", "st-reorder-meta", "car-reorder"} {
		as := accountPlans(t, seg, g[fam], 96)
		for i := 1; i < len(as); i++ {
			// Every tensor/block is already stored: only recipe cost remains.
			if as[i].NewBytes > 4096 {
				t.Errorf("%s r%d: struct-seg stored %d new bytes for a pure re-layout", fam, i, as[i].NewBytes)
			}
		}
	}
	// Data moved under CDC: reordering the data section costs real bytes.
	cdc := accountPlans(t, repByName("cdc"), g["st-reorder-data"], 96)
	st := accountPlans(t, seg, g["st-reorder-data"], 96)
	if cdc[1].NewBytes <= st[1].NewBytes {
		t.Errorf("expected CDC to pay more than struct-seg for a tensor re-layout: cdc=%d seg=%d", cdc[1].NewBytes, st[1].NewBytes)
	}
	// Appending a ZIP member reuses every existing payload under struct-seg.
	za := accountPlans(t, seg, g["zip-stored-add"], 96)
	if za[1].ReusedComponents < 8 {
		t.Errorf("zip-stored-add reused %d components", za[1].ReusedComponents)
	}
}

func TestDeltaModesReconstructExactly(t *testing.T) {
	g := genFamilies(t, "st-finetune-dense", "st-modify-sparse", "pagedb-update", "protorec-update")
	for fam, revs := range g {
		rep := accountPlans(t, repByName("delta-replace"), revs, 96)
		x := accountPlans(t, repByName("delta-xor"), revs, 96)
		xz := accountPlans(t, repByName("delta-xorz"), revs, 96)
		seg := accountPlans(t, repByName("struct-seg"), revs, 96)
		for i := 1; i < len(revs); i++ {
			if rep[i].NewBytes != seg[i].NewBytes {
				t.Errorf("%s r%d: delta-replace (%d) must equal struct-seg (%d)", fam, i, rep[i].NewBytes, seg[i].NewBytes)
			}
			if x[i].NewBytes > rep[i].NewBytes {
				t.Errorf("%s r%d: raw XOR %d exceeds replacement %d", fam, i, x[i].NewBytes, rep[i].NewBytes)
			}
			if xz[i].NewBytes > x[i].NewBytes {
				t.Errorf("%s r%d: compressed XOR %d exceeds raw XOR %d", fam, i, xz[i].NewBytes, x[i].NewBytes)
			}
		}
		if fam == "st-finetune-dense" && xz[1].NewBytes*2 > rep[1].NewBytes {
			t.Errorf("sparse low-bit perturbation should compress >2x under XOR: %d vs %d", xz[1].NewBytes, rep[1].NewBytes)
		}
	}
}

func tinyPages(edit func(b []byte)) (prev, cur []byte) {
	rng := corpus.NewRNG(5, "pages")
	prev = make([]byte, 4096*6)
	copy(prev, "ZPDB")
	binary.LittleEndian.PutUint32(prev[4:], 4096)
	rng.Fill(prev[4096:])
	cur = append([]byte(nil), prev...)
	edit(cur)
	return
}

func TestXORDeltaHandBuilt(t *testing.T) {
	prev, cur := tinyPages(func(b []byte) { b[2*4096+10] ^= 0x01; b[2*4096+4000] ^= 0xF0 })
	in0 := &Input{Family: "f", Format: "pagedb", Revision: "r0", Src: BytesSource("r0", prev), Opts: DefaultOptions()}
	in1 := &Input{Family: "f", Format: "pagedb", Revision: "r1", Src: BytesSource("r1", cur), Prev: in0, Opts: DefaultOptions()}
	ac := NewAcct(0, false)
	for _, mode := range []deltaMode{deltaXOR, deltaXORZ} {
		ac = NewAcct(0, false)
		p0, err := analyzeDelta(in0, mode)
		if err != nil {
			t.Fatal(err)
		}
		ac.Add(p0)
		p1, err := analyzeDelta(in1, mode)
		if err != nil {
			t.Fatal(err)
		}
		a1, _ := ac.Add(p1)
		var buf bytes.Buffer
		if err := p1.Rebuild(&buf, ac.Get); err != nil || !bytes.Equal(buf.Bytes(), cur) {
			t.Fatalf("mode %d: rebuild failed: %v", mode, err)
		}
		// Five unchanged pages are references; exactly one delta blob is new.
		if a1.NewComponents != 1 || a1.ReusedComponents != 5 {
			t.Errorf("mode %d: %+v", mode, a1)
		}
		if mode == deltaXOR && a1.NewBytes != 4096 {
			t.Errorf("raw XOR delta is a full page: %d", a1.NewBytes)
		}
		if mode == deltaXORZ && a1.NewBytes >= 100 {
			t.Errorf("two-byte XOR should compress to a few dozen bytes, got %d", a1.NewBytes)
		}
	}
}

func TestDeltaOversizedSegmentIsOptimisationFailure(t *testing.T) {
	prev, cur := tinyPages(func(b []byte) { b[5000] ^= 1 })
	in0 := &Input{Family: "f", Format: "pagedb", Revision: "r0", Src: BytesSource("r0", prev), Opts: Options{InlineMax: 512, MaxSegBytes: 1024}}
	in1 := &Input{Family: "f", Format: "pagedb", Revision: "r1", Src: BytesSource("r1", cur), Prev: in0, Opts: in0.Opts}
	if _, err := analyzeDelta(in1, deltaXORZ); err == nil || !strings.Contains(err.Error(), "exceeds delta limit") {
		t.Fatalf("expected limit error, got %v", err)
	}
	if _, err := analyzeRefMask(&Input{Format: "zbin", Src: BytesSource("z", make([]byte, 100)), Opts: in0.Opts}, false); err == nil {
		t.Fatal("malformed/oversized image accepted")
	}
}

func testCfg(dir string) RunConfig {
	c := DefaultRunConfig(dir)
	c.RangeReads = 2
	return c
}

func tinyCorpus(t *testing.T, fams ...string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := corpus.DefaultConfig()
	cfg.SizeKiB = 64
	cfg.Revisions = 2
	cfg.Families = fams
	if _, err := corpus.WriteDir(cfg, dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestMeasureDetectsReconstructionFailure(t *testing.T) {
	dir := tinyCorpus(t, "st-modify-sparse")
	base := repByName("cdc")
	cases := map[string]func(p *Plan){
		"flipped byte": func(p *Plan) {
			p.Rebuild = func(w io.Writer, get func(ID) ([]byte, error)) error {
				var buf bytes.Buffer
				if err := ConcatRebuild(p)(&buf, get); err != nil {
					return err
				}
				b := buf.Bytes()
				b[len(b)/2] ^= 1
				_, err := w.Write(b)
				return err
			}
		},
		"truncated": func(p *Plan) {
			p.Rebuild = func(w io.Writer, get func(ID) ([]byte, error)) error {
				var buf bytes.Buffer
				ConcatRebuild(p)(&buf, get)
				_, err := w.Write(buf.Bytes()[:buf.Len()-1])
				return err
			}
		},
		"extra byte": func(p *Plan) {
			p.Rebuild = func(w io.Writer, get func(ID) ([]byte, error)) error {
				ConcatRebuild(p)(w, get)
				_, err := w.Write([]byte{0})
				return err
			}
		},
		"rebuild error": func(p *Plan) {
			p.Rebuild = func(io.Writer, func(ID) ([]byte, error)) error { return io.ErrUnexpectedEOF }
		},
		"corrupt stored payload": func(p *Plan) {
			c := *p.Pieces[0].Comp
			orig := c.Data
			c.Data = func() ([]byte, error) {
				b, err := orig()
				if err == nil {
					b = append([]byte(nil), b...)
					b[0] ^= 0x80
				}
				return b, err
			}
			p.Pieces[0].Comp = &c
		},
	}
	for name, tamper := range cases {
		bad := Rep{Name: "broken", Analyze: func(in *Input) (*Plan, error) {
			p, err := base.Analyze(in)
			if err == nil {
				tamper(p)
			}
			return p, err
		}}
		rep, err := Run(func() RunConfig { c := testCfg(dir); return c }())
		_ = rep
		if err != nil {
			t.Fatal(err)
		}
		m, _ := corpus.ReadManifest(dir)
		_, by := m.Group()
		mem := by["st-modify-sparse"][1]
		p, _ := corpus.MemberPath(dir, mem)
		src, cl, _ := OpenSource(p)
		defer cl.Close()
		in := &Input{Family: mem.Family, Format: mem.Format, Revision: mem.Revision, Src: src, Opts: DefaultOptions()}
		row, ok := measure(testCfg(dir), bad, NewAcct(96, true), in, mem, mem.SHA256)
		if !ok || row.Status != "invalid" || row.RoundtripExact != "NO" {
			t.Errorf("%s: reconstruction failure not detected: status=%s rt=%s err=%q", name, row.Status, row.RoundtripExact, row.Error)
		}
	}
}

func TestAnalyzeFailureIsNotInvalidity(t *testing.T) {
	dir := tinyCorpus(t, "raw-insert")
	m, _ := corpus.ReadManifest(dir)
	mem := m.Members[0]
	p, _ := corpus.MemberPath(dir, mem)
	src, cl, _ := OpenSource(p)
	defer cl.Close()
	in := &Input{Family: mem.Family, Format: mem.Format, Src: src, Opts: DefaultOptions()}
	row, ok := measure(testCfg(dir), Rep{Name: "x", Analyze: func(*Input) (*Plan, error) { return nil, fmt.Errorf("parser gave up") }}, NewAcct(1, false), in, mem, mem.SHA256)
	if !ok || row.Status != "error" || row.RoundtripExact != "N/A" {
		t.Fatalf("%+v", row)
	}
	if _, ok := measure(testCfg(dir), Rep{Name: "n/a", Analyze: func(*Input) (*Plan, error) { return nil, ErrNotApplicable }}, NewAcct(1, false), in, mem, mem.SHA256); ok {
		t.Fatal("not-applicable representation produced a row")
	}
}

func TestRunSanityAcrossFormats(t *testing.T) {
	fams := []string{"raw-insert", "gzip-L6-insert-mid", "zip-deflate-change", "st-rename", "gguf-metadata", "torch-pkl-only",
		"bin-rebase", "car-overlap", "pagedb-append", "colstore-update-col", "ndarray-region-update", "protorec-reorder"}
	dir := tinyCorpus(t, fams...)
	cfg := testCfg(dir)
	rep, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p := Problems(rep); len(p) > 0 {
		t.Fatalf("rows not ok: %v", p)
	}
	type key struct{ fam, rep string }
	cum := map[key]int64{}
	logical := map[string]int64{}
	reps := map[string]map[string]bool{}
	for _, r := range rep.Rows {
		k := key{r.Family, r.Rep}
		cum[k] += r.Incremental
		if r.Cumulative != cum[k] {
			t.Fatalf("%s/%s %s: cumulative %d, running sum %d", r.Family, r.Revision, r.Rep, r.Cumulative, cum[k])
		}
		if r.Incremental != r.NewBytes+r.MetaBytes+r.IndexBytes {
			t.Fatalf("incremental accounting broken: %+v", r)
		}
		if r.Rep == "raw" {
			logical[fmt.Sprint(r.Family, r.Revision)] = r.LogicalSize
			if r.NewBytes != r.LogicalSize || r.ReuseBytes != 0 || r.MetaBytes != cfg.RefBytes {
				t.Fatalf("raw control must store every revision whole: %+v", r)
			}
		}
		if r.Rep == "cdc" && r.Index == 0 && r.RoundtripExact != "yes" {
			t.Fatalf("cdc r0 not exact: %+v", r)
		}
		if r.Oracle != (strings.HasPrefix(r.Rep, "oracle")) {
			t.Fatalf("oracle flag wrong for %s", r.Rep)
		}
		if r.Oracle && (r.RoundtripExact != "N/A" || r.ReconstructMs != nil) {
			t.Fatalf("oracle claims reconstruction: %+v", r)
		}
		if !r.Oracle && r.RoundtripExact != "yes" {
			t.Fatalf("%s/%s %s roundtrip %s", r.Family, r.Revision, r.Rep, r.RoundtripExact)
		}
		if reps[r.Format] == nil {
			reps[r.Format] = map[string]bool{}
		}
		reps[r.Format][r.Rep] = true
	}
	// applicability
	for _, tc := range []struct {
		format, rep string
		want        bool
	}{
		{"gzip", "struct-seg", false}, {"gzip", "oracle-logical", true}, {"zip", "oracle-logical", true},
		{"zbin", "oracle-refmask", true}, {"raw", "struct-seg", false}, {"safetensors", "oracle-refmask", false},
	} {
		if reps[tc.format] != nil && reps[tc.format][tc.rep] != tc.want {
			t.Errorf("%s/%s applicability = %v", tc.format, tc.rep, reps[tc.format][tc.rep])
		}
	}
	// comparisons are consistent with the rows they reference
	byKey := map[string]Row{}
	for _, r := range rep.Rows {
		byKey[r.Family+r.Revision+r.Rep] = r
	}
	for _, r := range rep.Rows {
		c := byKey[r.Family+r.Revision+"cdc"]
		if r.VsCDCBytes == nil || *r.VsCDCBytes != c.Incremental-r.Incremental {
			t.Fatalf("vs_cdc inconsistent for %s/%s %s", r.Family, r.Revision, r.Rep)
		}
		if r.VsRawBytes != byKey[r.Family+r.Revision+"raw"].Incremental-r.Incremental {
			t.Fatalf("vs_raw inconsistent")
		}
	}
}

func TestRangeReadsAreMeasuredAndAmplified(t *testing.T) {
	dir := tinyCorpus(t, "raw-insert")
	cfg := testCfg(dir)
	cfg.Reps = []string{"cdc", "fixed-4k", "raw"}
	rep, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Rows {
		if r.RangeReads == 0 || r.RangeAmplification == nil || *r.RangeAmplification < 1 {
			t.Fatalf("%s %s: range reads %d amp %v", r.Rep, r.Revision, r.RangeReads, r.RangeAmplification)
		}
	}
}

func TestRunRejectsBadConfigAndTamperedCorpus(t *testing.T) {
	dir := tinyCorpus(t, "raw-append")
	cfg := testCfg(dir)
	cfg.Reps = []string{"no-such-rep"}
	if _, err := Run(cfg); err == nil {
		t.Error("unknown representation accepted")
	}
	cfg = testCfg(dir)
	cfg.RefBytes = -1
	if _, err := Run(cfg); err == nil {
		t.Error("negative ref bytes accepted")
	}
	m, _ := corpus.ReadManifest(dir)
	p, _ := corpus.MemberPath(dir, m.Members[1])
	b, _ := os.ReadFile(p)
	b[0] ^= 1
	os.WriteFile(p, b, 0o644)
	if _, err := Run(testCfg(dir)); err == nil {
		t.Error("corpus file not matching its manifest was measured")
	}
	if _, err := Run(testCfg(t.TempDir())); err == nil {
		t.Error("missing manifest accepted")
	}
}

func scrub(r *Report) {
	for i := range r.Rows {
		x := &r.Rows[i]
		x.BuildMs, x.AllocBytes, x.ReconstructMs, x.ReconstructMBps = 0, 0, nil, nil
	}
}

func TestJSONReportIsStableAndComplete(t *testing.T) {
	dir := tinyCorpus(t, "zip-stored-meta", "bin-rebase")
	a, err := Run(testCfg(dir))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Run(testCfg(dir))
	scrub(a)
	scrub(b)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if !bytes.Equal(ja, jb) {
		t.Fatal("two runs over the same corpus differ beyond timing")
	}
	path := t.TempDir() + "/r.json"
	if err := WriteJSON(path, a); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var generic struct {
		Schema string           `json:"schema"`
		Rows   []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	if generic.Schema != ReportSchema || len(generic.Rows) != len(a.Rows) || len(a.Rows) == 0 {
		t.Fatalf("schema %q rows %d/%d", generic.Schema, len(generic.Rows), len(a.Rows))
	}
	required := []string{"family", "revision", "format", "representation", "logical_size", "incremental_bytes", "cumulative_bytes",
		"reuse_bytes", "metadata_bytes", "build_time_ms", "reconstruct_time_ms", "roundtrip_exact", "vs_cdc_bytes", "new_semantic_bytes",
		"incremental_packed_bytes", "status"}
	nullSeen := false
	for _, row := range generic.Rows {
		for _, k := range required {
			v, ok := row[k]
			if !ok {
				t.Fatalf("row missing %q: %v", k, row)
			}
			if v == nil {
				nullSeen = true // N/A is null, never a made-up zero
			}
		}
		if row["representation"] == "oracle-refmask" && (row["reconstruct_time_ms"] != nil || row["roundtrip_exact"] != "N/A") {
			t.Fatalf("oracle row invents reconstruction data: %v", row)
		}
	}
	if !nullSeen {
		t.Error("no N/A (null) fields seen; oracles should leave reconstruction fields null")
	}
	var tbl strings.Builder
	WriteTable(&tbl, a, true)
	for _, want := range []string{"zip", "zbin", "cdc", "struct-seg", "vsCDC", "oracle", "ranked"} {
		if !strings.Contains(tbl.String(), want) {
			t.Errorf("table lacks %q", want)
		}
	}
}
