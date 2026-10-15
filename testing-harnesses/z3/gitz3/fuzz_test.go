package gitz3

import (
	"bytes"
	"fmt"
	"testing"
)

var fuzzLim = Limits{MaxObjects: 4096, MaxObjectSize: 1 << 20, MaxInflated: 1 << 22, MaxDeltaDepth: 64, MaxDeltaOut: 1 << 22, MaxPack: 1 << 22}

func FuzzParsePack(f *testing.F) {
	for _, a := range algos {
		for _, v := range []string{"ofs", "ref"} {
			f.Add(byte(a), true, fx(f, fmt.Sprintf("%s-%s.pack", a, v)))
		}
		f.Add(byte(a), true, fx(f, fmt.Sprintf("%s-thin.pack", a)))
	}
	f.Fuzz(func(t *testing.T, algo byte, repair bool, data []byte) {
		a := SHA1
		if algo&1 == 1 {
			a = SHA256
		}
		if repair && len(data) > a.Size() {
			data = append([]byte(nil), data...)
			fixSum(a, data)
		}
		p, err := ParsePack(data, a, fuzzLim)
		if err != nil {
			return
		}
		if _, err := p.Resolve(); err != nil {
			return
		}
		bi, err := BuildIdx(p)
		if err != nil {
			t.Fatal(err)
		}
		x, err := ParseIdx(bi, a, fuzzLim)
		if err != nil {
			t.Fatalf("own index rejected: %v", err)
		}
		if err := x.CheckAgainstPack(p); err != nil {
			t.Fatal(err)
		}
	})
}

func FuzzDelta(f *testing.F) {
	base := []byte("hello world, hello git delta")
	f.Add(base, []byte{byte(len(base)), 11, 0x80 | 0x10, 5, 2, '!', '!', 0x80 | 0x01 | 0x10, 12, 4})
	f.Add(base, []byte{byte(len(base)), 0xff, 0xff, 0xff, 0x7f, 0x80})
	f.Fuzz(func(t *testing.T, base, d []byte) {
		out, err := ApplyDelta(base, d, 1<<20)
		st, serr := ScanDelta(d)
		if err == nil {
			if serr != nil || int(st.Tgt) != len(out) {
				t.Fatalf("apply ok but scan %v (%+v) out=%d", serr, st, len(out))
			}
		}
		if len(out) > 1<<20 {
			t.Fatal("output over bound")
		}
	})
}

func FuzzParseIdx(f *testing.F) {
	for _, a := range algos {
		f.Add(byte(a), true, fx(f, fmt.Sprintf("%s-ofs.idx", a)))
	}
	f.Fuzz(func(t *testing.T, algo byte, repair bool, data []byte) {
		a := SHA1
		if algo&1 == 1 {
			a = SHA256
		}
		if repair && len(data) > a.Size() {
			data = append([]byte(nil), data...)
			copy(data[len(data)-a.Size():], a.Sum(data[:len(data)-a.Size()]))
		}
		x, err := ParseIdx(data, a, fuzzLim)
		if err != nil {
			return
		}
		for i := 0; i < x.N; i++ {
			if pos, _ := x.Lookup(x.ID(i)); pos != i {
				t.Fatalf("lookup %d -> %d", i, pos)
			}
			x.Offset(i)
			x.CRC(i)
		}
	})
}

func FuzzParseLoose(f *testing.F) {
	for _, a := range algos {
		for _, ty := range []string{"blob", "tree", "commit", "tag"} {
			f.Add(byte(a), fx(f, fmt.Sprintf("%s-loose-%s", a, ty)))
		}
	}
	f.Fuzz(func(t *testing.T, algo byte, data []byte) {
		a := SHA1
		if algo&1 == 1 {
			a = SHA256
		}
		lo, err := ParseLoose(a, data, fuzzLim)
		if err != nil {
			return
		}
		if !bytes.Equal(lo.ID, a.Sum(lo.Canon)) || lo.Size != int64(len(lo.Payload)) {
			t.Fatal("inconsistent loose")
		}
	})
}
