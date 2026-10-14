package tdelta

import (
	"bytes"
	"crypto/sha256"
	"math/rand"
	"testing"

	"zeros3-z3/st"
)

// special fills b with bit patterns a float-converting codec would damage:
// NaN payloads, signed zeros, subnormals, infinities.
func special(r *rand.Rand, b []byte, w int) {
	for e := 0; e+w <= len(b); e += w {
		switch r.Intn(8) {
		case 0: // all-ones exponent, random payload (NaN)
			r.Read(b[e : e+w])
			b[e+w-1] |= 0x7f
			if w >= 2 {
				b[e+w-2] |= 0x80
			}
		case 1: // negative zero
			for j := range b[e : e+w] {
				b[e+j] = 0
			}
			b[e+w-1] = 0x80
		case 2: // subnormal: zero exponent, small mantissa
			for j := range b[e : e+w] {
				b[e+j] = 0
			}
			b[e] = byte(1 + r.Intn(255))
		}
	}
}

func TestPXRRoundtripAndCanonical(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for _, w := range []int{1, 2, 4, 8} {
		for _, frac := range []float64{0, 0.002, 0.3, 1} {
			for _, n := range []int{0, w, 3 * w, 4096 * w, 4097*w + 1} {
				base := make([]byte, n)
				r.Read(base)
				special(r, base, w)
				tgt := append([]byte(nil), base...)
				for e := 0; e+w <= n; e += w {
					if r.Float64() < frac {
						tgt[e] ^= byte(1 + r.Intn(255))
						if r.Intn(5) == 0 {
							tgt[e+w-1] ^= 0x80
						}
					}
				}
				special(r, tgt[:n/2], w) // some elements replaced outright
				for _, bs := range [][]byte{base, nil} {
					p, err := EncodePXR(bs, tgt, w)
					if err != nil {
						t.Fatal(err)
					}
					got, err := DecodePXR(bs, p, n, w)
					if err != nil || !bytes.Equal(got, tgt) {
						t.Fatalf("w=%d frac=%v n=%d base=%v: roundtrip failed: %v", w, frac, n, bs != nil, err)
					}
					// canonical: the decoded value re-encodes to the very same payload
					p2, _ := EncodePXR(bs, got, w)
					if !bytes.Equal(p, p2) {
						t.Fatalf("w=%d n=%d: payload not canonical", w, n)
					}
				}
			}
		}
	}
}

func TestPXRMalformed(t *testing.T) {
	r := rand.New(rand.NewSource(8))
	n, w := 600, 2
	base := make([]byte, n)
	r.Read(base)
	tgt := append([]byte(nil), base...)
	for i := 0; i < n; i += 6 {
		tgt[i] ^= byte(1 + r.Intn(255))
	}
	p, _ := EncodePXR(base, tgt, w)
	for cut := 0; cut < len(p); cut++ {
		if got, err := DecodePXR(base, p[:cut], n, w); err == nil && len(got) != n {
			t.Fatalf("truncated payload returned %d bytes", len(got))
		}
	}
	for i := 0; i < 400; i++ {
		q := append([]byte(nil), p...)
		q[r.Intn(len(q))] ^= byte(1 << r.Intn(8))
		got, err := DecodePXR(base, q, n, w)
		if err != nil {
			continue
		}
		if len(got) != n {
			t.Fatalf("corrupt payload returned %d bytes", len(got))
		}
		// range-coder tails have slack, so a damaged stream may decode to the very
		// same symbols; what must hold is that whatever it decodes to is a real
		// output: re-encoding it gives a stream that decodes back to it.
		p2, _ := EncodePXR(base, got, w)
		if again, err := DecodePXR(base, p2, n, w); err != nil || !bytes.Equal(again, got) {
			t.Fatalf("accepted output does not re-encode")
		}
	}
	if _, err := DecodePXR(base[:10], p, n, w); err == nil {
		t.Fatal("short base accepted")
	}
}

func TestRowsRoundtrip(t *testing.T) {
	r := rand.New(rand.NewSource(9))
	for _, rows := range []int{2, 7, 64} {
		for _, rowBytes := range []int{2, 8, 130} {
			for _, minReuse := range []int{0, 1, 3, 100} {
				n := rows * rowBytes
				base := make([]byte, n)
				r.Read(base)
				tgt := append([]byte(nil), base...)
				for row := 0; row < rows; row++ {
					if r.Intn(3) == 0 {
						tgt[row*rowBytes+r.Intn(rowBytes)] ^= 0x41
					}
				}
				p, rs, err := EncodeRows(base, tgt, rowBytes, 2, minReuse)
				if err != nil {
					t.Fatal(err)
				}
				if rs.ReuseRows+rs.DeltaRows != rows {
					t.Fatalf("rows %d != reuse %d + delta %d", rows, rs.ReuseRows, rs.DeltaRows)
				}
				got, err := DecodeRows(base, p, n, rowBytes, 2)
				if err != nil || !bytes.Equal(got, tgt) {
					t.Fatalf("rows=%d rb=%d min=%d: %v", rows, rowBytes, minReuse, err)
				}
			}
		}
	}
}

func TestRowsMalformed(t *testing.T) {
	base := make([]byte, 40)
	for i := range base {
		base[i] = byte(i)
	}
	tgt := append([]byte(nil), base...)
	tgt[12] ^= 1
	p, _, _ := EncodeRows(base, tgt, 4, 2, 1)
	bad := map[string][]byte{
		"empty":       nil,
		"zero runs":   {0},
		"too many":    {200, 1},
		"sum short":   {3, 1, 1, 1},
		"sum long":    {3, 5, 6, 5},
		"zero middle": {3, 3, 0, 7},
		"junk":        append(append([]byte(nil), p...), 9, 9, 9, 9, 9, 9, 9, 9),
	}
	for name, q := range bad {
		if _, err := DecodeRows(base, q, 40, 4, 2); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// a truncated payload may still be a valid (zero-padded) stream, but never the wrong length
	if got, err := DecodeRows(base, p[:len(p)-1], 40, 4, 2); err == nil && len(got) != 40 {
		t.Errorf("truncated recipe returned %d bytes", len(got))
	}
	// all rows reused must carry no payload
	if _, err := DecodeRows(base, []byte{1, 10, 7}, 40, 4, 2); err == nil {
		t.Error("all-reuse recipe with trailing bytes accepted")
	}
	if got, err := DecodeRows(base, []byte{1, 10}, 40, 4, 2); err != nil || !bytes.Equal(got, base) {
		t.Errorf("all-reuse recipe: %v", err)
	}
}

func TestEncodeTensorModes(t *testing.T) {
	r := rand.New(rand.NewSource(10))
	sp := Spec{Shape: []int64{64, 32}, W: 2}
	n := 64 * 32 * 2
	base := make([]byte, n)
	r.Read(base)
	check := func(name string, tgt, b []byte, want Mode) {
		rec, _, err := EncodeTensor(sp, b, tgt, Options{})
		if err != nil {
			t.Fatal(err)
		}
		m, payload, rest, err := ParseWire(rec, n)
		if err != nil || len(rest) != 0 {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if m != want {
			t.Errorf("%s: mode %v, want %v", name, m, want)
		}
		got, err := DecodeTensor(sp, b, m, payload, n)
		if err != nil || !bytes.Equal(got, tgt) {
			t.Fatalf("%s: decode: %v", name, err)
		}
	}
	check("same", base, base, ModeSame)
	rnd := make([]byte, n)
	r.Read(rnd)
	check("random target stays raw", rnd, base, ModeRaw)
	d := append([]byte(nil), base...)
	for i := 0; i < n; i += 2 {
		d[i] ^= byte(r.Intn(4))
	}
	check("dense small delta", d, base, ModeDelta)
	rows := append([]byte(nil), base...)
	for _, row := range []int{3, 4, 40} {
		for i := 0; i < 64; i += 2 {
			rows[row*64+i] ^= byte(1 + r.Intn(255))
		}
	}
	check("few changed rows", rows, base, ModeRows)
	check("no base, redundant", make([]byte, n), nil, ModeStandalone)
}

// file fixture: base + variants exercising every container path.
func fixture(r *rand.Rand) (base, tgt []byte) {
	mk := func(n, w int) []byte {
		b := make([]byte, n*w)
		r.Read(b)
		special(r, b, w)
		return b
	}
	embed, norm, head := mk(50*16, 2), mk(16, 4), mk(16*16, 2)
	meta := []st.KV{{K: "format", V: "pt"}}
	base = st.Build(meta, []st.BuildTensor{
		{Name: "embed", DType: "BF16", Shape: []int64{50, 16}, Data: embed},
		{Name: "norm", DType: "F32", Shape: []int64{16}, Data: norm},
		{Name: "head", DType: "F16", Shape: []int64{16, 16}, Data: head},
		{Name: "gone", DType: "F32", Shape: []int64{4}, Data: mk(4, 4)},
	})
	e2 := append([]byte(nil), embed...)
	for _, row := range []int{1, 2, 30} {
		for i := 0; i < 32; i += 2 {
			e2[row*32+i] ^= byte(1 + r.Intn(255))
		}
	}
	n2 := append([]byte(nil), norm...)
	for i := 0; i < len(n2); i += 4 {
		n2[i] ^= byte(r.Intn(3)) // low mantissa nudge
	}
	tgt = st.Build(meta, []st.BuildTensor{
		{Name: "embed", DType: "BF16", Shape: []int64{50, 16}, Data: e2},
		{Name: "norm", DType: "F32", Shape: []int64{16}, Data: n2},
		{Name: "head", DType: "F16", Shape: []int64{16, 16}, Data: head},        // identical
		{Name: "new", DType: "F32", Shape: []int64{8}, Data: mk(8, 4)},          // no base counterpart
		{Name: "reshaped", DType: "F16", Shape: []int64{8, 4}, Data: mk(32, 2)}, // base has none
	})
	return
}

func TestFileExactReconstruction(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	base, tgt := fixture(r)
	bf, err := st.ParseBytes(base, st.Options{})
	if err != nil {
		t.Fatal(err)
	}
	tf, err := st.ParseBytes(tgt, st.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, withBase := range []bool{true, false} {
		b, bff := base, bf
		if !withBase {
			b, bff = nil, nil
		}
		cont, rep, err := EncodeFile(tgt, tf, b, bff, Options{Compare: true})
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeFile(cont, b, bff)
		if err != nil {
			t.Fatalf("withBase=%v: %v", withBase, err)
		}
		if len(got) != len(tgt) || sha256.Sum256(got) != sha256.Sum256(tgt) || !bytes.Equal(got, tgt) {
			t.Fatalf("withBase=%v: reconstruction differs", withBase)
		}
		if withBase && rep.Total >= len(tgt) {
			t.Errorf("no saving: %d >= %d", rep.Total, len(tgt))
		}
		// the report accounts for every byte of the container
		sum := rep.Fixed + rep.Header
		for _, tr := range rep.Tensors {
			sum += tr.Wire
		}
		if sum != len(cont) {
			t.Errorf("report sums to %d, container is %d", sum, len(cont))
		}
		if withBase {
			var modes = map[string]Mode{}
			for _, tr := range rep.Tensors {
				modes[tr.Name] = tr.Mode
			}
			if modes["head"] != ModeSame || modes["embed"] != ModeRows {
				t.Errorf("modes %v", modes)
			}
		}
	}
	// a different base is refused, a truncated container is refused, never a wrong file
	cont, _, _ := EncodeFile(tgt, tf, base, bf, Options{})
	other := append([]byte(nil), base...)
	other[len(other)-1] ^= 1
	of, _ := st.ParseBytes(other, st.Options{})
	if _, err := DecodeFile(cont, other, of); err == nil {
		t.Error("wrong base accepted")
	}
	for cut := 0; cut < len(cont); cut += 7 {
		if got, err := DecodeFile(cont[:cut], base, bf); err == nil && !bytes.Equal(got, tgt) {
			t.Fatalf("truncated container at %d produced a different file", cut)
		}
	}
}

func FuzzDecodeFile(f *testing.F) {
	r := rand.New(rand.NewSource(12))
	base, tgt := fixture(r)
	bf, _ := st.ParseBytes(base, st.Options{})
	tf, _ := st.ParseBytes(tgt, st.Options{})
	cont, _, _ := EncodeFile(tgt, tf, base, bf, Options{})
	f.Add(cont)
	f.Add(cont[:len(cont)/2])
	old := maxFile
	maxFile = 1 << 20
	f.Cleanup(func() { maxFile = old })
	f.Fuzz(func(t *testing.T, b []byte) {
		got, err := DecodeFile(b, base, bf)
		if err != nil {
			return
		}
		// whatever decodes must be a valid Safetensors file that the container reproduces
		g, perr := st.ParseBytes(got, st.Options{AllowUnknownDType: true})
		if perr != nil {
			t.Fatalf("decoded bytes are not a Safetensors file: %v", perr)
		}
		c2, _, err := EncodeFile(got, g, base, bf, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if again, err := DecodeFile(c2, base, bf); err != nil || !bytes.Equal(again, got) {
			t.Fatalf("re-encode of decoded output does not round trip: %v", err)
		}
	})
}
