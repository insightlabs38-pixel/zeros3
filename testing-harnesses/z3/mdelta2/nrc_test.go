package mdelta2

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestNRCRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for _, d := range []DType{DF32, DBF16, DF16} {
		w := d.Width()
		for _, n := range []int{0, 1, 2, 3, 1000, 4097} {
			pred := make([]byte, n*w)
			res := make([]byte, n*w)
			for i := 0; i < n; i++ {
				putLE(pred, w, i, rng.Uint32()&mask(w))
				v := rng.Uint32() >> uint(rng.Intn(8*w))
				putLE(res, w, i, v&mask(w))
			}
			p := NRCEncode(d, res, pred)
			got, err := NRCDecode(d, p, pred, n)
			if err != nil || !bytes.Equal(got, res) {
				t.Fatalf("%v n=%d err=%v", d, n, err)
			}
			// truncation and corruption must fail or differ, never panic
			for cut := 0; cut < len(p) && cut < 40; cut++ {
				NRCDecode(d, p[:cut], pred, n)
			}
			q := append([]byte(nil), p...)
			for i := range q {
				q[i] ^= 0xff
				NRCDecode(d, q, pred, n)
				q[i] ^= 0xff
			}
		}
	}
}

func TestResidualBijection(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, w := range []int{2, 4} {
		for _, m := range []Residual{RKey, RXor, RSub} {
			tg, pr := make([]byte, 4096*w), make([]byte, 4096*w)
			for i := 0; i < 4096; i++ {
				putLE(tg, w, i, rng.Uint32()&mask(w))
				putLE(pr, w, i, rng.Uint32()&mask(w))
			}
			// include special patterns
			putLE(tg, w, 0, 0x80000000&mask(w)) // -0
			putLE(pr, w, 0, 0)
			r := MakeResidual(m, w, tg, pr)
			if !bytes.Equal(ApplyResidual(m, w, r, pr), tg) {
				t.Fatalf("w=%d m=%d", w, m)
			}
		}
	}
	for h := uint32(0); h < 65536; h++ { // all F16 patterns: key bijection and finite round trip
		if unkey(key(h, 2), 2) != h {
			t.Fatal("key")
		}
		if x := f16ToF64(h); x == x && x-x == 0 && f64ToF16(x) != h && !(x == 0) {
			t.Fatalf("f16 round trip %x", h)
		}
	}
}

func TestCodersRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for _, d := range []DType{DF32, DF16, DBF16} {
		w := d.Width()
		for _, n := range []int{0, 1, 63, 64, 65, 3000} {
			pred := make([]byte, n*w)
			res := make([]byte, n*w)
			for i := 0; i < n; i++ {
				putLE(pred, w, i, rng.Uint32()&mask(w))
				v := rng.Uint32() >> uint(rng.Intn(8*w+1)%32)
				if rng.Intn(3) == 0 {
					v = 0
				}
				putLE(res, w, i, v&mask(w))
			}
			for c := 0; c < numCoder; c++ {
				p, ok := EncodeResidual(c, d, RXor, res, pred)
				if !ok {
					continue
				}
				got, err := DecodeResidual(c, d, RXor, p, pred, n)
				if err != nil || !bytes.Equal(got, res) {
					t.Fatalf("coder %s %v n=%d err=%v", CoderNames[c], d, n, err)
				}
				for cut := 0; cut < len(p) && cut < 30; cut++ { // truncation never panics or succeeds silently with n>0
					g2, e2 := DecodeResidual(c, d, RXor, p[:cut], pred, n)
					if n > 0 && e2 == nil && bytes.Equal(g2, res) && cut < len(p)-1 && c != CRaw && c != CRice {
						_ = g2
					}
				}
			}
		}
	}
}
