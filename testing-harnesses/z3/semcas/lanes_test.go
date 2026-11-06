package semcas

import (
	"bytes"
	"math"
	"os"
	"testing"

	"zeros3-z3/st"
)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func replayAll(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	s2, err := Open(dir) // fresh open: only serialized records, blobs and recipes
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		rc, err := s2.GetRecipe(PayloadSHA(data))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var out bytes.Buffer
		if err := s2.Replay(rc, &out, 1<<20); err != nil {
			t.Fatalf("%s replay: %v", name, err)
		}
		if !bytes.Equal(out.Bytes(), data) {
			t.Fatalf("%s: reconstructed bytes differ", name)
		}
	}
}

func TestTinyFamilyAcrossFourFormats(t *testing.T) {
	files := map[string][]byte{}
	for _, n := range []string{"tiny.safetensors", "tiny.pt", "tiny.gguf", "tiny.onnx"} {
		files[n] = fixture(t, n)
	}
	s := newStore(t)
	opt := DefaultOptions()
	opt.MinBytes = 64
	p := NewPlanner(s, opt)
	stats := map[string]*FileStats{}
	for _, n := range []string{"tiny.safetensors", "tiny.pt", "tiny.gguf", "tiny.onnx"} {
		doc, err := Detect(n, files[n])
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		rc, fs, err := p.Ingest(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutRecipe(rc); err != nil {
			t.Fatal(err)
		}
		stats[n] = fs
	}
	// PyTorch: w, b, and the strided view wt whose storage is w's bytes: all identity.
	if st := stats["tiny.pt"]; st.How["identity"] != st.Tensors {
		t.Fatalf("pytorch tensors not shared: %v", st.How)
	}
	if st := stats["tiny.gguf"]; st.How["identity"] != st.Tensors {
		t.Fatalf("gguf tensors not shared: %v", st.How)
	}
	// ONNX stores the weight transposed: exact layout relation, no new payload bytes.
	if st := stats["tiny.onnx"]; st.NewBlobBytes != 0 || st.How["layout"] != 1 {
		t.Fatalf("onnx transposed weight not derived: %+v", st.How)
	}
	replayAll(t, s.Dir, files)
}

func TestTensorOrderShardAndHeaderIndependence(t *testing.T) {
	a := rnd(8192, 1)
	b := rnd(4096, 2)
	c := rnd(6000, 3)
	mk := func(meta []st.KV, ts ...st.BuildTensor) []byte { return st.Build(meta, ts) }
	ta := st.BuildTensor{Name: "a", DType: "U8", Shape: []int64{8192}, Data: a}
	tb := st.BuildTensor{Name: "b", DType: "U8", Shape: []int64{4096}, Data: b}
	tc := st.BuildTensor{Name: "c", DType: "U8", Shape: []int64{6000}, Data: c}
	full := mk([]st.KV{{K: "format", V: "pt"}}, ta, tb, tc)
	reordered := mk(nil, tc, ta, tb)
	shard1 := mk([]st.KV{{K: "shard", V: "1"}}, ta)
	shard2 := mk(nil, tb, tc)
	files := map[string][]byte{"full": full, "reordered": reordered, "s1": shard1, "s2": shard2}
	s := newStore(t)
	p := NewPlanner(s, DefaultOptions())
	var newBytes []int64
	for _, n := range []string{"full", "reordered", "s1", "s2"} {
		doc, err := Detect(n, files[n])
		if err != nil {
			t.Fatal(err)
		}
		rc, fs, err := p.Ingest(doc)
		if err != nil {
			t.Fatal(err)
		}
		s.PutRecipe(rc)
		newBytes = append(newBytes, fs.NewBlobBytes)
	}
	if newBytes[1] != 0 || newBytes[2] != 0 || newBytes[3] != 0 {
		t.Fatalf("reordered/sharded repackaging stored new payload bytes: %v", newBytes)
	}
	replayAll(t, s.Dir, files)
}

// Same shape and dtype with a different payload must not share.
func TestSameShapeDifferentPayload(t *testing.T) {
	s := newStore(t)
	p := NewPlanner(s, DefaultOptions())
	x := rnd(8192, 1)
	y := append([]byte(nil), x...)
	y[4000] ^= 1
	a, _ := p.Place(&TInfo{DType: DU8, Shape: []uint64{64, 128}}, x)
	b, _ := p.Place(&TInfo{DType: DU8, Shape: []uint64{64, 128}}, y)
	if a.CID == b.CID {
		t.Fatal("different payloads share an identity")
	}
	if b.How == "identity" || b.How == "bytes-shared" {
		t.Fatalf("different payload reported as %s", b.How)
	}
}

// The cross-dtype relation must reproduce signed zeros and NaNs through its residual.
func TestConvertRelationKeepsNaNAndSignedZero(t *testing.T) {
	n := 4096
	bf := make([]byte, 2*n)
	rng := newRand(11)
	for i := 0; i < n; i++ {
		binaryPut16(bf[2*i:], f32toBF16(math.Float32bits(float32(rng.NormFloat64()*0.05))))
	}
	binaryPut16(bf[0:], 0x8000) // -0
	binaryPut16(bf[2:], 0x7fc1) // quiet NaN with payload
	binaryPut16(bf[4:], 0x7f81) // signaling NaN
	binaryPut16(bf[6:], 0x7f80) // +Inf
	s := newStore(t)
	p := NewPlanner(s, DefaultOptions())
	if _, err := p.Place(&TInfo{DType: DBF16, Shape: []uint64{uint64(n)}}, bf); err != nil {
		t.Fatal(err)
	}
	f16, _ := convert(bf, DBF16, DF16)
	// make the target differ from the prediction at a NaN so the residual is exercised
	binaryPut16(f16[4:], 0x7c01)
	pl, err := p.Place(&TInfo{DType: DF16, Shape: []uint64{uint64(n)}}, f16)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Payload(pl.CID)
	if err != nil || !bytes.Equal(got, f16) {
		t.Fatalf("converted payload differs (%s): %v", pl.How, err)
	}
	if pl.How == "new" {
		t.Fatalf("relation not found")
	}
	if u := uint16(got[0]) | uint16(got[1])<<8; u != 0x8000 {
		t.Fatalf("negative zero changed to %04x", u)
	}
}

func FuzzScanTorchPickle(f *testing.F) {
	pt := fixture(f, "tiny.pt")
	if d, err := ParseTorch("x", pt); err == nil {
		for _, s := range d.Segs {
			_ = s
		}
	}
	f.Add([]byte("\x80\x02}q\x00."))
	f.Add([]byte("\x80\x02ctorch._utils\n_rebuild_tensor_v2\nq\x00."))
	f.Fuzz(func(t *testing.T, b []byte) { ScanTorchPickle(b) })
}

func FuzzParseONNX(f *testing.F) {
	f.Add(fixture(f, "tiny.onnx"))
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := ParseONNX("f.onnx", b)
		if err == nil {
			if e := d.Check(); e != nil {
				t.Fatal(e)
			}
		}
	})
}

// Partial reads must equal the original bytes for every derivation shape.
func TestRangeReadsMatchOriginal(t *testing.T) {
	s := newStore(t)
	opt := DefaultOptions()
	opt.MinBytes = 64
	p := NewPlanner(s, opt)
	// base BF16 and derived F16, Q8_0, transposed F32 and row-permuted F16 variants
	rng := newRand(5)
	rows, cols := 64, 96
	bf := make([]byte, 2*rows*cols)
	for i := 0; i < rows*cols; i++ {
		binaryPut16(bf[2*i:], f32toBF16(math.Float32bits(float32(rng.NormFloat64()*0.05))))
	}
	files := map[string][]byte{}
	mk := func(name string, dt int, shape []uint64, payload []byte) {
		var b Builder
		pl, err := p.Place(&TInfo{DType: dt, Shape: shape}, payload)
		if err != nil {
			t.Fatal(err)
		}
		b.Lit([]byte("HDR"))
		b.Ref(pl.CID, name)
		b.Lit([]byte("TAIL"))
		file := append(append([]byte("HDR"), payload...), "TAIL"...)
		b.R.Size, b.R.SHA = int64(len(file)), PayloadSHA(file)
		files[name] = file
		s.PutRecipe(&b.R)
		t.Logf("%s placed as %s", name, pl.How)
	}
	mk("bf16", DBF16, []uint64{64, 96}, bf)
	f32, _ := convert(bf, DBF16, DF32)
	f16, _ := convert(bf, DBF16, DF16)
	mk("f16", DF16, []uint64{64, 96}, f16)
	q8, _ := (&Op{Kind: OpQ8_0}).Apply([][]byte{f32})
	mk("q8", DQBase+8, []uint64{64, 96}, q8)
	tr, _ := (&Op{Kind: OpPerm, Elem: 4, Dims: []uint64{64, 96}, Axes: []uint8{1, 0}}).Apply([][]byte{f32})
	mk("f32T", DF32, []uint64{96, 64}, tr)
	g, _ := (&Op{Kind: OpPerm, Elem: 2 * 96, Dims: []uint64{4, 2, 8}, Axes: []uint8{0, 2, 1}}).Apply([][]byte{f16})
	mk("f16perm", DF16, []uint64{64, 96}, g)
	for name, file := range files {
		rc, err := s.GetRecipe(PayloadSHA(file))
		if err != nil {
			t.Fatal(err)
		}
		for it := 0; it < 300; it++ {
			off := int64(rng.Intn(len(file)))
			n := int64(rng.Intn(min(len(file)-int(off), 3000)) + 0)
			var io IO
			got, err := s.ReadRange(rc, off, n, &io)
			if err != nil || !bytes.Equal(got, file[off:off+n]) {
				t.Fatalf("%s range %d+%d: %v", name, off, n, err)
			}
		}
	}
}
