package numcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/rand"
	"testing"

	"zeros3-z3/qcore"
)

// ---- synthetic files and an in-memory store ----

type tfile struct {
	data []byte
	lay  *Layout
	id   ID
}

type tspec struct {
	name string
	dt   DType
	cols int64
	data []byte
}

func build(header []byte, ts []tspec, tail []byte) *tfile {
	f := &tfile{lay: &Layout{}}
	f.data = append(f.data, header...)
	for i, t := range ts {
		f.lay.T = append(f.lay.T, Tensor{Name: t.name, DT: t.dt, Off: int64(len(f.data)), Len: int64(len(t.data)), Cols: t.cols})
		f.data = append(f.data, t.data...)
		if i%2 == 0 {
			f.data = append(f.data, 0, 0, 0) // padding gap
		}
	}
	f.data = append(f.data, tail...)
	f.lay.Size = int64(len(f.data))
	f.id = sha256.Sum256(f.data)
	return f
}

func (f *tfile) src() Source { return RawSource{bytes.NewReader(f.data), f.lay} }

type memStore struct {
	obj  map[ID][]byte
	raw  map[ID]*tfile
	aux  map[ID][]byte
	fail error
}

func newStore() *memStore {
	return &memStore{obj: map[ID][]byte{}, raw: map[ID]*tfile{}, aux: map[ID][]byte{}}
}

func (s *memStore) Open(ctx context.Context, id ID) (Stored, error) {
	if s.fail != nil {
		return Stored{}, s.fail
	}
	if b, ok := s.obj[id]; ok {
		o, err := OpenObject(bytes.NewReader(b), int64(len(b)))
		return Stored{Obj: o}, err
	}
	if f, ok := s.raw[id]; ok {
		return Stored{Raw: bytes.NewReader(f.data), Lay: f.lay}, nil
	}
	return Stored{}, ErrMissing7
}

func (s *memStore) Aux(ctx context.Context, id ID) ([]byte, error) {
	if b, ok := s.aux[id]; ok {
		return b, nil
	}
	return nil, ErrMissing7
}

func encodeT(t *testing.T, f *tfile, bases []*tfile, o EncOpts, aux ...AuxIn) ([]byte, *EncStats) {
	t.Helper()
	var bi []BaseIn
	for _, b := range bases {
		bi = append(bi, BaseIn{ID: b.id, Len: b.lay.Size, Src: b.src()})
	}
	cands := func(ti int) []Cand {
		var out []Cand
		for s, b := range bases {
			for k := range b.lay.T {
				if b.lay.T[k].Name == f.lay.T[ti].Name {
					out = append(out, Cand{Slot: s, TIdx: k})
				}
			}
		}
		return out
	}
	var buf bytes.Buffer
	st, err := Encode(context.Background(), &buf, bytes.NewReader(f.data), f.lay, bi, cands, aux, o)
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), st
}

func decodeT(obj []byte, rv Resolver, opt DecOpts) ([]byte, error) {
	o, err := OpenObject(bytes.NewReader(obj), int64(len(obj)))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if _, err := o.DecodeTo(context.Background(), &out, rv, opt); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func f32s(r *rand.Rand, n int, scale float64) []byte {
	b := make([]byte, 4*n)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(float32(r.NormFloat64()*scale)))
	}
	return b
}

func bf16s(r *rand.Rand, n int) []byte {
	b := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(math.Float32bits(float32(r.NormFloat64()*0.05))>>16))
	}
	return b
}

// perturb adds small key-domain noise to a float payload (a fine-tune step).
func perturb(r *rand.Rand, src []byte, w int, frac float64) []byte {
	out := append([]byte(nil), src...)
	for i := 0; i < len(src)/w; i++ {
		if r.Float64() < frac {
			PutLE(out, w, i, Unkey(Key(LE(src, w, i), w)+uint32(r.Intn(7))-3, w)&Mask(w))
		}
	}
	return out
}

func family(r *rand.Rand) (*tfile, *tfile, *tfile) {
	special := make([]byte, 4*64) // NaNs, infinities, signed zeros, subnormals
	for i, v := range []uint32{0x7fc00001, 0xffc12345, 0x7f800000, 0xff800000, 0x80000000, 0, 1, 0x807fffff, 0x7f7fffff} {
		binary.LittleEndian.PutUint32(special[4*i:], v)
	}
	narrowable := make([]byte, 4*3000) // F32 holding exactly float16 values
	for i := 0; i < 3000; i++ {
		binary.LittleEndian.PutUint32(narrowable[4*i:], math.Float32bits(qcore.F16to32(qcore.F16(float32(r.NormFloat64())))))
	}
	ints := make([]byte, 5000)
	r.Read(ints[:2500])
	a := build([]byte(`{"hdr":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), []tspec{
		{"w0", DF32, 100, f32s(r, 5000, 0.02)},
		{"w1", DBF16, 64, bf16s(r, 4096)},
		{"sp", DF32, 0, special},
		{"nar", DF32, 0, narrowable},
		{"ids", DRaw, 0, ints},
		{"empty", DF32, 0, nil},
		{"h", DF16, 0, bf16s(r, 777)},
	}, []byte("tail"))
	b := build([]byte(`{"hdr":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"}`), []tspec{
		{"w0", DF32, 100, perturb(r, a.data[a.lay.T[0].Off:a.lay.T[0].Off+a.lay.T[0].Len], 4, 0.5)},
		{"w1", DBF16, 64, perturb(r, a.data[a.lay.T[1].Off:a.lay.T[1].Off+a.lay.T[1].Len], 2, 0.3)},
		{"sp", DF32, 0, special},
		{"nar", DF32, 0, perturb(r, narrowable, 4, 0)},
		{"ids", DRaw, 0, ints},
		{"empty", DF32, 0, nil},
		{"h", DF16, 0, bf16s(r, 777)},
	}, []byte("tail"))
	c := build([]byte(`{"hdr":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaac"}`), []tspec{
		{"w0", DF32, 100, perturb(r, b.data[b.lay.T[0].Off:b.lay.T[0].Off+b.lay.T[0].Len], 4, 0.5)},
		{"w1", DBF16, 64, perturb(r, b.data[b.lay.T[1].Off:b.lay.T[1].Off+b.lay.T[1].Len], 2, 0.3)},
		{"sp", DF32, 0, special},
		{"nar", DF32, 0, narrowable},
		{"ids", DRaw, 0, ints},
		{"empty", DF32, 0, nil},
		{"h", DF16, 0, bf16s(r, 777)},
	}, []byte("tail"))
	return a, b, c
}

func TestZN7ChainRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	a, b, c := family(r)
	st := newStore()
	for _, win := range []int64{1 << 20, 512, 100} {
		for _, workers := range []int{1, 4} {
			o := EncOpts{Window: win, Workers: workers}
			oa, sa := encodeT(t, a, nil, o)
			ob, sb := encodeT(t, b, []*tfile{a}, o)
			oc, _ := encodeT(t, c, []*tfile{b, a}, o)
			st.obj[a.id], st.obj[b.id], st.obj[c.id] = oa, ob, oc
			if sa.Narrowed == 0 || sb.ModeCount[MPred] == 0 || sb.ModeCount[MCopy] == 0 {
				t.Fatalf("win %d: expected narrowing, prediction and copies: %+v %+v", win, sa, sb)
			}
			for _, f := range []*tfile{a, b, c} {
				got, err := decodeT(st.obj[f.id], st, DecOpts{Workers: workers})
				if err != nil || !bytes.Equal(got, f.data) {
					t.Fatalf("win %d workers %d: replay failed: %v", win, workers, err)
				}
			}
			// byte-identical objects regardless of worker count
			o2 := o
			o2.Workers = 1
			if x, _ := encodeT(t, c, []*tfile{b, a}, o2); !bytes.Equal(x, oc) {
				t.Fatalf("win %d: object depends on worker count", win)
			}
		}
	}
	// raw (original) bases resolve as well
	st2 := newStore()
	st2.raw[a.id] = a
	ob, _ := encodeT(t, b, []*tfile{a}, EncOpts{Window: 300})
	if got, err := decodeT(ob, st2, DecOpts{}); err != nil || !bytes.Equal(got, b.data) {
		t.Fatalf("raw base: %v", err)
	}
}

func TestZN7Ranges(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	a, b, c := family(r)
	st := newStore()
	o := EncOpts{Window: 256}
	st.obj[a.id], _ = encodeT(t, a, nil, o)
	st.obj[b.id], _ = encodeT(t, b, []*tfile{a}, o)
	st.obj[c.id], _ = encodeT(t, c, []*tfile{b}, o)
	obj, _ := OpenObject(bytes.NewReader(st.obj[c.id]), int64(len(st.obj[c.id])))
	n := int64(len(c.data))
	check := func(off, ln int64) {
		got, _, err := obj.ReadRange(context.Background(), off, ln, st, DecOpts{})
		if err != nil || !bytes.Equal(got, c.data[off:off+ln]) {
			t.Fatalf("range %d+%d: %v", off, ln, err)
		}
	}
	for _, tt := range c.lay.T { // every tensor and window boundary
		for _, d := range []int64{-3, -1, 0, 1, 1024, 1025} {
			for _, ln := range []int64{0, 1, 2, 1023, 1024, 4000} {
				off := tt.Off + d
				if off >= 0 && off+ln <= n {
					check(off, ln)
				}
			}
		}
	}
	for i := 0; i < 300; i++ {
		off := r.Int63n(n)
		check(off, r.Int63n(n-off+1))
	}
	check(0, n)
	for _, bad := range [][2]int64{{-1, 1}, {0, n + 1}, {n, 1}, {math.MaxInt64, 2}, {5, -1}} {
		if _, _, err := obj.ReadRange(context.Background(), bad[0], bad[1], st, DecOpts{}); !errors.Is(err, ErrRange7) {
			t.Fatalf("range %v accepted: %v", bad, err)
		}
	}
	// a small range must not decode the whole object
	_, stt, _ := obj.ReadRange(context.Background(), c.lay.T[0].Off+40, 16, st, DecOpts{})
	if stt.Windows > 3 {
		t.Fatalf("4-element range decoded %d windows", stt.Windows)
	}
}

func TestZN7StructuredAndDepth(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	rows, cols := 96, 80
	base := bf16s(r, rows*cols)
	// rank-2 update and a pure scaling, both rounded to BF16 (exact structure plus rounding)
	u, v := make([]float64, rows*2), make([]float64, cols*2)
	for i := range u {
		u[i] = r.NormFloat64() * 0.05
	}
	for i := range v {
		v[i] = r.NormFloat64() * 0.05
	}
	low, scl := make([]byte, len(base)), make([]byte, len(base))
	bb := Base{DT: DBF16, Data: base}
	for i := 0; i < rows; i++ {
		for j := 0; j < cols; j++ {
			x := bb.at(i*cols + j)
			PutLE(low, 2, i*cols+j, F64ToBF16(x+u[2*i]*v[2*j]+u[2*i+1]*v[2*j+1]))
			PutLE(scl, 2, i*cols+j, F64ToBF16(x*0.97))
		}
	}
	a := build([]byte("H"), []tspec{{"m", DBF16, int64(cols), base}, {"s", DBF16, int64(cols), base}}, nil)
	b := build([]byte("H"), []tspec{{"m", DBF16, int64(cols), low}, {"s", DBF16, int64(cols), scl}}, nil)
	plain, _ := encodeT(t, b, []*tfile{a}, EncOpts{Window: 1000})
	str, sst := encodeT(t, b, []*tfile{a}, EncOpts{Window: 1000, Structured: true})
	if sst.Structured != 2 || len(str)*2 > len(plain) {
		t.Fatalf("structured predictors not selected or not effective: %d tensors, %d vs %d bytes", sst.Structured, len(str), len(plain))
	}
	st := newStore()
	st.raw[a.id] = a
	if got, err := decodeT(str, st, DecOpts{}); err != nil || !bytes.Equal(got, b.data) {
		t.Fatalf("structured replay: %v", err)
	}
	// natural noise must not trigger a structured fit
	n := build([]byte("H"), []tspec{{"m", DBF16, int64(cols), perturb(r, base, 2, 0.9)}, {"s", DBF16, int64(cols), base}}, nil)
	if _, s := encodeT(t, n, []*tfile{a}, EncOpts{Structured: true}); s.Structured != 0 || s.FullEvals != 0 {
		t.Fatalf("probe let a noise update through: %+v", s)
	}

	// depth: a chain longer than MaxHops7 re-roots at encode and is refused at decode
	st = newStore()
	chain := []*tfile{a}
	depth := []int{0}
	st.obj[a.id], _ = encodeT(t, a, nil, EncOpts{})
	for i := 1; i <= 5; i++ {
		prev := chain[i-1]
		cur := build([]byte("H"), []tspec{{"m", DBF16, int64(cols), perturb(r, prev.data[1:1+len(base)], 2, 0.2)}}, nil)
		d := depth[i-1]
		var buf bytes.Buffer
		es, err := Encode(context.Background(), &buf, bytes.NewReader(cur.data), cur.lay, []BaseIn{{ID: prev.id, Len: prev.lay.Size, Src: prev.src(), Depth: func(int) int { return d }, GapDepth: d}},
			func(int) []Cand { return []Cand{{Slot: 0, TIdx: 0}} }, nil, EncOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if es.Depth[0] > MaxHops7 {
			t.Fatalf("state %d depth %d", i, es.Depth[0])
		}
		chain, depth = append(chain, cur), append(depth, es.Depth[0])
		st.obj[cur.id] = buf.Bytes()
		if got, err := decodeT(buf.Bytes(), st, DecOpts{}); err != nil || !bytes.Equal(got, cur.data) {
			t.Fatalf("state %d: %v", i, err)
		}
	}
	if depth[3] != 3 || depth[4] != 0 {
		t.Fatalf("depths %v: expected re-rooting after three hops", depth)
	}
	// forge an over-deep chain: encode against a claimed depth of 0 at every step
	st = newStore()
	st.obj[a.id], _ = encodeT(t, a, nil, EncOpts{})
	prev := a
	var last []byte
	for i := 0; i < MaxDepth7; i++ {
		cur := build([]byte("H"), []tspec{{"m", DBF16, int64(cols), perturb(r, prev.data[1:1+len(base)], 2, 0.2)}}, nil)
		last, _ = encodeT(t, cur, []*tfile{prev}, EncOpts{})
		st.obj[cur.id] = last
		prev = cur
	}
	if _, err := decodeT(last, st, DecOpts{}); !errors.Is(err, ErrDepth7) {
		t.Fatalf("over-deep chain: %v", err)
	}
	// self reference (cycle) ends at the depth bound
	cyc := newStore()
	ob, _ := encodeT(t, b, []*tfile{a}, EncOpts{})
	cyc.obj[a.id] = ob // a "is" b's object: wrong identity
	if _, err := decodeT(ob, cyc, DecOpts{}); err == nil {
		t.Fatal("wrong base identity accepted")
	}
}

func quantFamily(t *testing.T, r *rand.Rand) (*tfile, *tfile, AuxIn) {
	rows, cols := 24, 256
	fl := bf16s(r, rows*cols)
	x := floatsOf(Base{DT: DBF16, Data: fl}, false)
	qw := make([]float32, cols)
	for i := range qw {
		qw[i] = float32(math.Exp(r.NormFloat64()))
	}
	// imatrix file with entries for two tensors
	var im []byte
	im = binary.LittleEndian.AppendUint32(im, 2)
	for _, name := range []string{"q4k", "q40"} {
		im = binary.LittleEndian.AppendUint32(im, uint32(len(name)))
		im = append(im, name...)
		im = binary.LittleEndian.AppendUint32(im, 1)
		im = binary.LittleEndian.AppendUint32(im, uint32(cols))
		for _, v := range qw {
			im = binary.LittleEndian.AppendUint32(im, math.Float32bits(v))
		}
	}
	q := func(qt qcore.Type, w []float32, ar qcore.Arith) []byte {
		n, _ := qcore.OutLen(qt, len(x), cols)
		out := make([]byte, n)
		if err := qcore.Quantize(out, qt, x, cols, w, ar); err != nil {
			t.Fatal(err)
		}
		return out
	}
	q4k := q(qcore.Q4_K, qw, qcore.GCCFMA)
	q40 := q(qcore.Q4_0, qw, qcore.Strict)
	q8 := q(qcore.Q8_0, nil, qcore.Strict)
	q6 := q(qcore.Q6_K, nil, qcore.Strict)
	copy(q6[210*5:], bytes.Repeat([]byte{0xa5}, 210)) // one block the quantizer cannot explain
	unk := make([]byte, 110*8)                        // an IQ3_S-like opaque block type
	r.Read(unk)
	src := build([]byte("FLOATHDR"), []tspec{{"q4k", DBF16, int64(cols), fl}, {"q40", DBF16, int64(cols), fl}, {"q8", DBF16, int64(cols), fl}, {"q6", DBF16, int64(cols), fl}}, nil)
	tgt := build([]byte("QHDR"), []tspec{{"q4k", DQ4_K, int64(cols), q4k}, {"q40", DQ4_0, int64(cols), q40}, {"q8", DQ8_0, int64(cols), q8}, {"q6", DQ6_K, int64(cols), q6}, {"unk", DQOther, 0, unk}}, nil)
	return src, tgt, AuxIn{ID: sha256.Sum256(im), Data: im}
}

func TestZN7Quant(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	src, tgt, aux := quantFamily(t, r)
	obj, st := encodeT(t, tgt, []*tfile{src}, EncOpts{Window: 2048}, aux)
	if st.QuantPatch != 1 || st.ModeCount[MQuant] == 0 || st.ModeBytes[MQuant] > 400 {
		t.Fatalf("quant derivation: %+v", st)
	}
	store := newStore()
	store.raw[src.id] = src
	store.aux[aux.ID] = aux.Data
	if got, err := decodeT(obj, store, DecOpts{Workers: 3}); err != nil || !bytes.Equal(got, tgt.data) {
		t.Fatalf("quant replay: %v", err)
	}
	// without the imatrix the weighted tensors are not derivable: stored opaquely, still exact
	obj2, st2 := encodeT(t, tgt, []*tfile{src}, EncOpts{})
	if st2.ModeBytes[MRaw] < st.ModeBytes[MRaw]+1000 {
		t.Fatalf("expected opaque fallback without imatrix: %+v", st2)
	}
	if got, err := decodeT(obj2, store, DecOpts{}); err != nil || !bytes.Equal(got, tgt.data) {
		t.Fatalf("fallback replay: %v", err)
	}
	// missing or altered imatrix fails closed
	delete(store.aux, aux.ID)
	if _, err := decodeT(obj, store, DecOpts{}); !errors.Is(err, ErrMissing7) {
		t.Fatalf("missing aux: %v", err)
	}
	bad := append([]byte(nil), aux.Data...)
	bad[len(bad)-1] ^= 1
	store.aux[aux.ID] = bad
	if _, err := decodeT(obj, store, DecOpts{}); err == nil {
		t.Fatal("altered aux accepted")
	}
	// altered float source: digest mismatch, never wrong bytes
	store.aux[aux.ID] = aux.Data
	src2 := *src
	src2.data = append([]byte(nil), src.data...)
	src2.data[src.lay.T[0].Off+10] ^= 0x40
	store.raw[src.id] = &src2
	if got, err := decodeT(obj, store, DecOpts{}); err == nil && !bytes.Equal(got, tgt.data) {
		t.Fatal("silent wrong bytes from an altered base")
	} else if err == nil {
		t.Fatal("altered base not detected")
	}
}

// Every single-byte corruption and every truncation of an object must fail or decode exactly.
func TestZN7CorruptionNeverWrongBytes(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	a, b, _ := family(r)
	qs, qt, aux := quantFamily(t, r)
	st := newStore()
	st.raw[a.id], st.raw[qs.id] = a, qs
	st.aux[aux.ID] = aux.Data
	ob, _ := encodeT(t, b, []*tfile{a}, EncOpts{Window: 700})
	oq, _ := encodeT(t, qt, []*tfile{qs}, EncOpts{Window: 2048}, aux)
	for _, c := range []struct {
		obj  []byte
		want []byte
	}{{ob, b.data}, {oq, qt.data}} {
		step := 1
		if len(c.obj) > 20000 {
			step = 3
		}
		for i := 0; i < len(c.obj); i += step {
			m := append([]byte(nil), c.obj...)
			m[i] ^= 1 << uint(i%8)
			if got, err := decodeT(m, st, DecOpts{}); err == nil && !bytes.Equal(got, c.want) {
				t.Fatalf("flip at %d of %d: accepted wrong bytes", i, len(c.obj))
			}
		}
		for n := 0; n < len(c.obj); n += 1 + len(c.obj)/400 {
			if got, err := decodeT(c.obj[:n], st, DecOpts{}); err == nil && !bytes.Equal(got, c.want) {
				t.Fatalf("truncation to %d accepted", n)
			}
		}
	}
	// corrupt, missing and failing dependencies
	st2 := newStore()
	if _, err := decodeT(ob, st2, DecOpts{}); !errors.Is(err, ErrMissing7) {
		t.Fatalf("missing base: %v", err)
	}
	a2 := *a
	a2.data = append([]byte(nil), a.data...)
	a2.data[a.lay.T[0].Off+8] ^= 2
	st2.raw[a.id] = &a2
	if _, err := decodeT(ob, st2, DecOpts{}); !errors.Is(err, ErrAuth7) {
		t.Fatalf("corrupt base: %v", err)
	}
	st2.fail = io.ErrClosedPipe
	if _, err := decodeT(ob, st2, DecOpts{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("resolver failure not propagated: %v", err)
	}
}

type shortW struct{ n int }

func (w *shortW) Write(p []byte) (int, error) {
	if w.n -= len(p); w.n < 0 {
		return 0, io.ErrShortWrite
	}
	return len(p), nil
}

type shortR struct {
	r   io.ReaderAt
	max int64
}

func (s shortR) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > s.max {
		n, _ := s.r.ReadAt(p[:maxI64(0, s.max-off)], off)
		return n, nil // a short read without an error
	}
	return s.r.ReadAt(p, off)
}

func TestZN7ShortIOAndCancel(t *testing.T) {
	r := rand.New(rand.NewSource(6))
	a, b, _ := family(r)
	st := newStore()
	st.raw[a.id] = a
	ob, _ := encodeT(t, b, []*tfile{a}, EncOpts{Window: 500})
	obj, _ := OpenObject(bytes.NewReader(ob), int64(len(ob)))
	for _, n := range []int{0, 10, len(b.data) / 2, len(b.data) - 1} {
		if _, err := obj.DecodeTo(context.Background(), &shortW{n: n}, st, DecOpts{Workers: 2}); err == nil {
			t.Fatalf("short writer at %d ignored", n)
		}
	}
	for _, n := range []int{0, 30, len(b.data) / 3} { // encoder: short target reads and short writes
		_, err := Encode(context.Background(), io.Discard, shortR{bytes.NewReader(b.data), int64(n)}, b.lay, nil, nil, nil, EncOpts{})
		if err == nil {
			t.Fatalf("short target read at %d ignored", n)
		}
		if _, err := Encode(context.Background(), &shortW{n: n}, bytes.NewReader(b.data), b.lay, nil, nil, nil, EncOpts{}); err == nil {
			t.Fatalf("short object write at %d ignored", n)
		}
	}
	if _, err := OpenObject(shortR{bytes.NewReader(ob), int64(len(ob) - 5)}, int64(len(ob))); err == nil {
		t.Fatal("short directory read ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := obj.DecodeTo(ctx, io.Discard, st, DecOpts{Workers: 2}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled decode: %v", err)
	}
	if _, err := Encode(ctx, io.Discard, bytes.NewReader(b.data), b.lay, nil, nil, nil, EncOpts{Workers: 2}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled encode: %v", err)
	}
}

func TestZN7HostileLayoutsAndDirectories(t *testing.T) {
	bad := []*Layout{
		nil,
		{Size: -1},
		{Size: 10, T: []Tensor{{DT: DF32, Off: 4, Len: 8}}},                               // beyond the file
		{Size: 100, T: []Tensor{{DT: DF32, Off: 0, Len: 6}}},                              // not whole elements
		{Size: 100, T: []Tensor{{DT: DF32, Off: 8, Len: 8}, {DT: DF32, Off: 12, Len: 8}}}, // overlap
		{Size: 100, T: []Tensor{{DT: DF32, Off: math.MaxInt64 - 2, Len: 8}}},              // overflow
		{Size: 100, T: []Tensor{{DT: DQ4_K, Off: 0, Len: 100}}},                           // not whole blocks
		{Size: 100, T: []Tensor{{DT: DF32, Off: 0, Len: 8, Cols: -4}}},
	}
	for i, l := range bad {
		if _, err := Encode(context.Background(), io.Discard, bytes.NewReader(make([]byte, 128)), l, nil, nil, nil, EncOpts{}); err == nil {
			t.Fatalf("layout %d accepted", i)
		}
	}
	// directories announcing absurd counts and lengths
	r := rand.New(rand.NewSource(7))
	a, _, _ := family(r)
	oa, _ := encodeT(t, a, nil, EncOpts{Window: 128})
	dirOff := binary.LittleEndian.Uint64(oa[len(oa)-zTrailLen:])
	for i := 0; i < 4000; i++ {
		m := append([]byte(nil), oa...)
		p := int(dirOff) + r.Intn(len(oa)-int(dirOff))
		switch r.Intn(3) {
		case 0:
			m[p] = byte(r.Intn(256))
		case 1:
			copy(m[p:], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f})
		default:
			m[p] ^= 0x80
		}
		if got, err := decodeT(m, newStore(), DecOpts{}); err == nil && !bytes.Equal(got, a.data) {
			t.Fatalf("hostile directory %d accepted wrong bytes", i)
		}
	}
	for _, tr := range [][]byte{nil, []byte("ZN7"), bytes.Repeat([]byte{0xff}, 64), append([]byte(zMagic), bytes.Repeat([]byte{0xff}, 40)...)} {
		if _, err := OpenObject(bytes.NewReader(tr), int64(len(tr))); err == nil {
			t.Fatalf("garbage object %q opened", tr)
		}
	}
}

func FuzzOpenObject(f *testing.F) {
	r := rand.New(rand.NewSource(8))
	a, b, _ := family(r)
	oa, _ := encodeT(&testing.T{}, a, nil, EncOpts{Window: 512})
	ob, _ := encodeT(&testing.T{}, b, []*tfile{a}, EncOpts{Window: 512})
	f.Add(oa[len(oa)-600:])
	f.Add(ob[len(ob)-600:])
	st := newStore()
	st.raw[a.id] = a
	f.Fuzz(func(t *testing.T, tail []byte) {
		// splice a fuzzed tail (directory + trailer region) onto a valid payload area
		m := append(append([]byte(nil), ob[:len(ob)/2]...), tail...)
		if got, err := decodeT(m, st, DecOpts{}); err == nil && !bytes.Equal(got, b.data) {
			t.Fatal("fuzzed object decoded to wrong bytes")
		}
		if got, err := decodeT(tail, st, DecOpts{}); err == nil && !bytes.Equal(got, b.data) && !bytes.Equal(got, a.data) {
			t.Fatal("fuzzed object decoded to wrong bytes")
		}
	})
}

func FuzzPatches(f *testing.F) {
	f.Add([]byte{1, 0, 1, 2, 3, 4}, uint8(0))
	f.Add([]byte{2, 1, 9, 9, 9, 9, 1, 8, 8, 8, 8}, uint8(0))
	f.Fuzz(func(t *testing.T, p []byte, coder uint8) {
		dst := make([]byte, 4*16)
		_ = applyPatches(dst, p, coder&1, 4) // must not panic or run away
	})
}
