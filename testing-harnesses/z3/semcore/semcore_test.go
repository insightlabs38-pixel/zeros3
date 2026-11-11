package semcore

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"os"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(NewMemBackend())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func rnd(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func tdesc(dt int, shape ...uint64) Desc {
	return Desc{Kind: KindTensor, Version: 1, DType: uint16(dt), Shape: shape, Ordered: true}
}

// Same bytes, different types: byte-identical payload shares one blob, semantics stay apart.
func TestTypedIdentityNeverAliases(t *testing.T) {
	s := newStore(t)
	p := rnd(4096, 1)
	a, _ := s.PutRaw(tdesc(DF16, 2048), p)
	b, _ := s.PutRaw(tdesc(DI16, 2048), p)
	c, _ := s.PutRaw(tdesc(DF16, 32, 64), p)
	d, _ := s.PutRaw(Desc{Kind: KindBytes, Version: 1, DType: DU8, Shape: []uint64{4096}, Ordered: true}, p)
	ids := map[ID]bool{a.CID: true, b.CID: true, c.CID: true, d.CID: true}
	if len(ids) != 4 {
		t.Fatalf("expected 4 distinct semantic identities, got %d", len(ids))
	}
	if a.Payload != b.Payload || len(s.ByPayload(a.Payload)) != 4 {
		t.Fatal("byte-identical payloads must share one byte digest")
	}
	if n := len(s.B.(*MemBackend).blobs); n != 1 {
		t.Fatalf("one physical blob expected, got %d", n)
	}
	// the descriptor determines the payload length: a wrong length is rejected
	if _, err := s.PutRaw(tdesc(DF32, 2048), p); err == nil {
		t.Fatal("descriptor/payload length mismatch accepted")
	}
}

// Endianness is never inferred: big-endian bytes are a different value sequence.
func TestEndianNotInterchangeable(t *testing.T) {
	s := newStore(t)
	le := make([]byte, 4*2048)
	for i := 0; i < 2048; i++ {
		binary.LittleEndian.PutUint32(le[4*i:], math.Float32bits(float32(i)*0.5-100))
	}
	be, err := (&Op{Kind: OpBswap, Elem: 4}).Apply([][]byte{le})
	if err != nil {
		t.Fatal(err)
	}
	p := NewPlanner(s, DefaultOptions())
	if _, err := p.Place(&TInfo{DType: DF32, Shape: []uint64{2048}}, le); err != nil {
		t.Fatal(err)
	}
	// Presented as plain F32 the swapped bytes are unrelated content, not a duplicate.
	pl, err := p.Place(&TInfo{DType: DF32, Shape: []uint64{2048}}, be)
	if err != nil {
		t.Fatal(err)
	}
	if pl.How == "identity" || pl.How == "bytes-shared" {
		t.Fatalf("big-endian bytes aliased little-endian: %s", pl.How)
	}
	// An explicit, proven swap derivation reconstructs them exactly.
	base := s.ByPayload(PayloadSHA(le))[0]
	d := tdesc(DF32, 2048)
	d.Extra = []byte("be") // distinct canonical attribute => distinct identity
	r, err := s.PutDerived(d, []ID{base}, []Op{{Kind: OpBswap, Elem: 4}}, nil, be)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Payload(r.CID)
	if err != nil || !bytes.Equal(got, be) {
		t.Fatal("swap derivation does not reproduce big-endian bytes")
	}
}

func TestFloatConversionBitExact(t *testing.T) {
	// every half value: widen then narrow returns the same bits (NaN payload and signed zero kept)
	for h := 0; h < 1<<16; h++ {
		f := f16to32(uint16(h))
		got := f32to16(f)
		if snan := h&0x7c00 == 0x7c00 && h&0x3ff != 0 && h&0x200 == 0; snan {
			// signaling NaN: payload bits survive, narrowing quiets it (such a value needs an
			// exact residual in a derivation; it never aliases silently)
			if got != uint16(h)|0x200 {
				t.Fatalf("f16 snan %04x -> %04x", h, got)
			}
		} else if got != uint16(h) {
			t.Fatalf("f16 %04x -> f32 %08x -> %04x", h, f, got)
		}
		if h&0x7fff == 0 && f != uint32(h)<<16 {
			t.Fatalf("signed zero lost: %04x -> %08x", h, f)
		}
		// widening agrees with float32 arithmetic for finite values
		if e := (h >> 10) & 31; e != 31 {
			sign := 1.0
			if h&0x8000 != 0 {
				sign = -1
			}
			var want float64
			if e == 0 {
				want = sign * float64(h&1023) * math.Pow(2, -24)
			} else {
				want = sign * (1 + float64(h&1023)/1024) * math.Pow(2, float64(e-15))
			}
			if math.Float32frombits(f) != float32(want) && !(want == 0 && math.Float32frombits(f) == 0) {
				t.Fatalf("f16 %04x widens to %v want %v", h, math.Float32frombits(f), want)
			}
		}
	}
	// bf16 round trip through f32 and NaN quieting
	for h := 0; h < 1<<16; h++ {
		f := uint32(h) << 16
		if h&0x7f80 == 0x7f80 && h&0x7f != 0 { // NaN: payload kept, quiet bit forced
			if got := f32toBF16(f); got != uint16(h)|0x40 {
				t.Fatalf("bf16 nan %04x -> %04x", h, got)
			}
			continue
		}
		if got := f32toBF16(f); got != uint16(h) {
			t.Fatalf("bf16 %04x -> %04x", h, got)
		}
	}
	// rounding ties-to-even for narrowing
	if f32to16(0x3f801000) != 0x3c00 { // 1+2^-11: exactly halfway, even
		t.Fatal("tie to even")
	}
	if f32to16(0x3f801001) != 0x3c01 {
		t.Fatal("round up")
	}
}

func TestQuantizersMatchReference(t *testing.T) {
	raw, err := os.ReadFile("../semcas/testdata/quant_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]string
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	x, _ := hex.DecodeString(v["x"])
	for name, op := range map[string]Op{"q8_0": {Kind: OpQ8_0}, "q4_0": {Kind: OpQ4_0}} {
		want, _ := hex.DecodeString(v[name])
		got, err := op.Apply([][]byte{x})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s differs from the gguf-py reference", name)
		}
	}
}

func TestPermuteInverseProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for it := 0; it < 300; it++ {
		r := 2 + rng.Intn(3)
		dims := make([]uint64, r)
		n := uint64(1)
		for i := range dims {
			dims[i] = uint64(1 + rng.Intn(6))
			n *= dims[i]
		}
		elem := []uint32{1, 2, 4, 8, 3}[rng.Intn(5)]
		axes := make([]uint8, r)
		for i, a := range rng.Perm(r) {
			axes[i] = uint8(a)
		}
		op := Op{Kind: OpPerm, Elem: elem, Dims: dims, Axes: axes}
		data := rnd(int(n)*int(elem), int64(it))
		out, err := op.Apply([][]byte{data})
		if err != nil {
			t.Fatal(err)
		}
		inv := InvertPerm(op)
		back, err := inv.Apply([][]byte{out})
		if err != nil || !bytes.Equal(back, data) {
			t.Fatalf("perm %v %v elem %d not invertible", dims, axes, elem)
		}
		// element-level check against a direct index computation
		if r == 2 && axes[0] == 1 {
			for i := uint64(0); i < dims[0]; i++ {
				for j := uint64(0); j < dims[1]; j++ {
					src := (i*dims[1] + j) * uint64(elem)
					dst := (j*dims[0] + i) * uint64(elem)
					if !bytes.Equal(data[src:src+uint64(elem)], out[dst:dst+uint64(elem)]) {
						t.Fatal("2-D transpose element mismatch")
					}
				}
			}
		}
	}
}

func TestPatchesRoundTripAndBounds(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for it := 0; it < 200; it++ {
		a := rnd(1+rng.Intn(300), int64(it))
		b := append([]byte(nil), a...)
		for k := 0; k < rng.Intn(8); k++ {
			b[rng.Intn(len(b))] ^= byte(1 + rng.Intn(255))
		}
		ps := DiffPatches(a, b, 4)
		ser := marshalPatches(ps)
		back, n, err := parsePatches(ser, uint64(len(a)))
		if err != nil || n != len(ser) {
			t.Fatal(err)
		}
		c := append([]byte(nil), a...)
		if err := ApplyPatches(c, back); err != nil || !bytes.Equal(c, b) {
			t.Fatal("patch round trip")
		}
	}
	if _, _, err := parsePatches(marshalPatches([]Patch{{Off: 10, Data: []byte{1, 2, 3}}}), 11); err == nil {
		t.Fatal("patch past the output accepted")
	}
}

// A derivation must be proven; a wrong one never enters the store.
func TestDerivationProofAndRejection(t *testing.T) {
	s := newStore(t)
	f32 := make([]byte, 4*1024)
	for i := 0; i < 1024; i++ {
		binary.LittleEndian.PutUint32(f32[4*i:], math.Float32bits(float32(i)/7))
	}
	base, _ := s.PutRaw(tdesc(DF32, 1024), f32)
	want, _ := convert(f32, DF32, DF16)
	if _, err := s.PutDerived(tdesc(DF16, 1024), []ID{base.CID}, []Op{{Kind: OpConvert, From: DF32, To: DF16}}, nil, want); err != nil {
		t.Fatal(err)
	}
	bad := append([]byte(nil), want...)
	bad[10] ^= 1
	if _, err := s.PutDerived(tdesc(DF16, 1024), []ID{base.CID}, []Op{{Kind: OpConvert, From: DF32, To: DF16}}, nil, bad); err == nil {
		t.Fatal("derivation that does not reproduce the payload was accepted")
	}
	// exact residual repairs it
	ps := DiffPatches(want, bad, 4)
	if _, err := s.PutDerived(tdesc(DF16, 1024), []ID{base.CID}, []Op{{Kind: OpConvert, From: DF32, To: DF16}}, ps, bad); err != nil {
		t.Fatalf("residual-repaired derivation rejected: %v", err)
	}
}

func TestStoreCorruptionAndLimits(t *testing.T) {
	s := newStore(t)
	p := NewPlanner(s, DefaultOptions())
	data := make([]byte, 2*8192)
	for i := range data[:8192] {
		data[i] = byte(i)
	}
	copy(data[8192:], data[:8192])
	pl, _ := p.Place(&TInfo{DType: DU8, Shape: []uint64{8192}}, data[:8192])
	r := s.Rec(pl.CID)
	// corrupt blob
	mb := s.B.(*MemBackend)
	orig := append([]byte(nil), mb.blobs[r.Payload]...)
	mb.Corrupt(r.Payload, 0)
	if _, err := s.Payload(pl.CID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt blob accepted: %v", err)
	}
	mb.blobs[r.Payload] = append([]byte(nil), orig...)
	// missing blob
	mb.DeleteBlob(r.Payload)
	if _, err := s.Payload(pl.CID); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing blob: %v", err)
	}
	mb.blobs[r.Payload] = orig
	// record bit flips are caught by parse or identity check
	ser := r.Marshal()
	for i := range ser {
		m := append([]byte(nil), ser...)
		m[i] ^= 0x41
		rr, err := ParseRecord(m)
		if err == nil && rr.CID == r.CID {
			t.Fatalf("flip at %d produced an identical, accepted record", i)
		}
	}
	for n := 0; n < len(ser); n++ {
		if _, err := ParseRecord(ser[:n]); err == nil {
			t.Fatalf("truncated record %d accepted", n)
		}
	}
	// reopen rebuilds the index from records alone
	s2, err := NewStore(s.B)
	if err != nil || s2.Count() != s.Count() {
		t.Fatalf("reopen: %v", err)
	}
}

func TestDependencyCycleDepthAndFanout(t *testing.T) {
	s := newStore(t)
	payload := bytes.Repeat([]byte{7}, 64)
	d := tdesc(DU8, 64)
	// forge two derived records that name each other (a store can be corrupted by hand)
	a := &Record{Desc: d, PayloadLen: 64, Payload: PayloadSHA(payload), Ops: []Op{{Kind: OpSlice, Off: 0, Len: 64}}}
	a.CID = CID(&a.Desc, 64, a.Payload)
	d2 := tdesc(DU8, 32, 2)
	b := &Record{Desc: d2, PayloadLen: 64, Payload: PayloadSHA(payload), Ops: []Op{{Kind: OpSlice, Off: 0, Len: 64}}}
	b.CID = CID(&b.Desc, 64, b.Payload)
	a.Parents, b.Parents = []ID{b.CID}, []ID{a.CID}
	for _, r := range []*Record{a, b} {
		s.B.PutRecord(r.CID, r.Marshal())
	}
	s2, err := NewStore(s.B)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Payload(a.CID); !errors.Is(err, ErrDepth) {
		t.Fatalf("cycle not rejected: %v", err)
	}
	if _, err := s2.Closure(a.CID); !errors.Is(err, ErrDepth) {
		t.Fatalf("closure cycle not rejected: %v", err)
	}
	// fanout: more parents than the cap is unparseable
	big := &Record{Desc: d, PayloadLen: 64, Payload: PayloadSHA(payload), Ops: []Op{{Kind: OpConcat}}}
	for i := 0; i <= MaxFanout; i++ {
		big.Parents = append(big.Parents, ID{byte(i)})
	}
	if _, err := ParseRecord(big.Marshal()); err == nil {
		t.Fatal("fanout above the cap accepted")
	}
	// depth: a chain of MaxDepth+2 slices fails closed
	s3 := newStore(t)
	raw, _ := s3.PutRaw(tdesc(DU8, 64), payload)
	prev := raw
	var last *Record
	for i := 0; i < MaxDepth+2; i++ {
		dd := tdesc(DU8, uint64(2+i), 0)
		dd.Shape = []uint64{64, uint64(i + 1)}
		dd.Shape = []uint64{uint64(64 / 1), 1}
		dd.Extra = []byte{byte(i)}
		nr := &Record{Desc: Desc{Kind: KindTensor, Version: 1, DType: DU8, Shape: []uint64{64}, Ordered: true, Extra: []byte{byte(i)}}, PayloadLen: 64, Payload: PayloadSHA(payload), Parents: []ID{prev.CID}, Ops: []Op{{Kind: OpSlice, Off: 0, Len: 64}}}
		nr.CID = CID(&nr.Desc, 64, nr.Payload)
		s3.B.PutRecord(nr.CID, nr.Marshal())
		s3.index(nr)
		prev, last = nr, nr
	}
	if _, err := s3.Payload(last.CID); !errors.Is(err, ErrDepth) {
		t.Fatalf("over-deep chain not rejected: %v", err)
	}
}

func TestRecipeReplayFailsClosed(t *testing.T) {
	s := newStore(t)
	a := rnd(5000, 5)
	ra, _ := s.PutRaw(Desc{Kind: KindBytes, Version: 1, DType: DU8, Shape: []uint64{5000}, Ordered: true}, a)
	var b Builder
	b.Lit([]byte("HEAD"))
	b.Ref(ra.CID, "a")
	b.Span(ra.CID, 100, 50, "")
	file := append(append([]byte("HEAD"), a...), a[100:150]...)
	b.R.Size, b.R.SHA = int64(len(file)), PayloadSHA(file)
	ser := b.R.Marshal()
	rc, err := ParseRecipe(ser)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := s.Replay(rc, &out, 1<<20); err != nil || !bytes.Equal(out.Bytes(), file) {
		t.Fatalf("replay: %v", err)
	}
	rc.SHA[0] ^= 1
	if err := s.Replay(rc, &bytes.Buffer{}, 1<<20); !errors.Is(err, ErrMismatch) {
		t.Fatalf("wrong digest accepted: %v", err)
	}
	for n := 0; n < len(ser); n++ {
		if _, err := ParseRecipe(ser[:n]); err == nil {
			t.Fatalf("truncated recipe %d accepted", n)
		}
	}
	// oversized length claims
	huge := append([]byte(RecipeMagic), binary.AppendUvarint(nil, 1<<60)...)
	huge = append(huge, make([]byte, 32)...)
	huge = binary.AppendUvarint(huge, 1<<40)
	if _, err := ParseRecipe(huge); err == nil {
		t.Fatal("oversized step count accepted")
	}
	// span past the component fails
	bad := &Recipe{Size: 10, SHA: PayloadSHA(make([]byte, 10)), Steps: []Step{{Kind: StepSpan, CID: ra.CID, Off: 4990, Len: 10 + 20}}}
	if err := s.Replay(bad, &bytes.Buffer{}, 1<<20); err == nil {
		t.Fatal("span out of range accepted")
	}
	// a short writer is an error, not silent truncation
	rc2, _ := ParseRecipe(ser)
	if err := s.Replay(rc2, shortWriter{}, 1<<20); err == nil {
		t.Fatal("short writer ignored")
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

func FuzzParseRecord(f *testing.F) {
	s := &Store{}
	_ = s
	d := tdesc(DF16, 8)
	r := &Record{Desc: d, PayloadLen: 16, Payload: PayloadSHA(make([]byte, 16))}
	f.Add(r.Marshal())
	dr := &Record{Desc: d, PayloadLen: 16, Payload: PayloadSHA(make([]byte, 16)), Parents: []ID{{1}}, Ops: []Op{{Kind: OpConvert, From: DF32, To: DF16}, {Kind: OpGather, Elem: 4, Idx: []uint32{1, 0}}}, Patches: []Patch{{Off: 1, Data: []byte{9}}}}
	f.Add(dr.Marshal())
	f.Fuzz(func(t *testing.T, b []byte) {
		rec, err := ParseRecord(b)
		if err != nil {
			return
		}
		if again, err := ParseRecord(rec.Marshal()); err != nil || again.CID != rec.CID {
			t.Fatalf("accepted record does not re-parse identically: %v", err)
		}
	})
}

func FuzzParseRecipe(f *testing.F) {
	var b Builder
	b.Lit([]byte("x"))
	b.Ref(ID{1}, "")
	b.Span(ID{2}, 3, 4, "")
	b.R.Size = 6
	f.Add(b.R.Marshal())
	f.Fuzz(func(t *testing.T, in []byte) {
		r, err := ParseRecipe(in)
		if err != nil {
			return
		}
		if _, err := ParseRecipe(r.Marshal()); err != nil {
			t.Fatal(err)
		}
	})
}

func newRand(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

func binaryPut16(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }
