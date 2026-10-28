package bitcodez3

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/scientificz3"
)

func fixture(t testing.TB) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/native-clang19.bc")
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestNativeCountsAndExactGroups(t *testing.T) {
	data := fixture(t)
	raw, e := os.ReadFile("testdata/native-clang19.json")
	if e != nil {
		t.Fatal(e)
	}
	var meta struct {
		SHA256          string
		Blocks, Records map[uint64]int
	}
	if e = json.Unmarshal(raw, &meta); e != nil {
		t.Fatal(e)
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != meta.SHA256 {
		t.Fatal("native identity")
	}
	p, e := Parse(data)
	if e != nil {
		t.Fatal(e)
	}
	blocks, records := map[uint64]int{}, map[uint64]int{}
	for _, b := range p.Blocks {
		blocks[b.ID]++
	}
	for _, r := range p.Records {
		records[r.Block]++
	}
	if !reflect.DeepEqual(blocks, meta.Blocks) || !reflect.DeepEqual(records, meta.Records) {
		t.Fatal("native oracle disagreement")
	}
	for _, mode := range []string{"functions", "leaves", "records"} {
		b, e := Encode(data, p, mode)
		if e != nil {
			t.Fatal(e)
		}
		if e = kinds.ProveLimits(nil, b, data, repz3.Limits{MaxDepth: 4}); e != nil {
			t.Fatal(e)
		}
		st := repz3.NewMemStore()
		id, _, e := b.Persist(st)
		if e != nil {
			t.Fatal(e)
		}
		for _, off := range []int64{0, int64(len(data)) / 2} {
			env := kinds.NewEnvLimits(st, repz3.Limits{MaxDepth: 4, MaxWhole: 16 << 20})
			call, e := env.Open(id)
			if e != nil {
				t.Fatal(e)
			}
			got, e := scientificz3.ReadRange(call, off, 128)
			if e != nil || !bytes.Equal(got, data[off:off+128]) {
				t.Fatalf("fresh serialized range: %v", e)
			}
		}
	}
}

type writer struct {
	b []byte
	n uint64
}

func (w *writer) bits(x, n uint64) {
	for i := uint64(0); i < n; i++ {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		w.b[w.n/8] |= byte((x>>i)&1) << uint(w.n%8)
		w.n++
	}
}
func (w *writer) vbr(x, n uint64) {
	for x >= (1 << (n - 1)) {
		w.bits(x&((1<<(n-1))-1)|(1<<(n-1)), n)
		x >>= n - 1
	}
	w.bits(x, n)
}
func (w *writer) align() {
	for w.n%32 != 0 {
		w.bits(0, 1)
	}
}
func unknown() []byte {
	w := writer{}
	w.bits(0xdec04342, 32)
	w.bits(1, 2)
	w.vbr(999, 8)
	w.vbr(2, 4)
	w.align()
	w.bits(2, 32)
	w.bits(3, 2)
	w.vbr(77, 6)
	w.vbr(1, 6)
	w.vbr(42, 6)
	w.bits(0, 2)
	w.align()
	return w.b
}
func TestUnknownRecordsPreservedAndMalformedAbbreviations(t *testing.T) {
	b := unknown()
	p, e := Parse(b)
	if e != nil || len(p.Records) != 1 || p.Records[0].Code != 77 {
		t.Fatalf("unknown framing: %v", e)
	}
	for _, mode := range []string{"functions", "leaves", "records"} {
		out, e := Encode(b, p, mode)
		if e != nil {
			t.Fatal(e)
		}
		if e = kinds.ProveLimits(nil, out, b, repz3.Limits{MaxDepth: 4}); e != nil {
			t.Fatal(e)
		}
	}
	for _, enc := range []uint64{0, 3, 6, 7} {
		w := writer{}
		w.vbr(1, 5)
		w.bits(0, 1)
		w.bits(enc, 3)
		r := reader{data: w.b, end: w.n, info: map[uint64][]abbreviation{}}
		r.definition()
		if r.err == nil {
			t.Fatalf("invalid abbreviation encoding %d", enc)
		}
	}
	for _, n := range []int{0, 3, 7, len(b) - 1, len(b) - 4} {
		if _, e := Parse(b[:n]); e == nil {
			t.Fatal("truncated framing")
		}
	}
}
func FuzzGenericFraming(f *testing.F) {
	f.Add(fixture(f))
	f.Add(unknown())
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<20 {
			return
		}
		p, e := Parse(b)
		if e != nil {
			return
		}
		for _, mode := range []string{"functions", "leaves", "records"} {
			out, e := Encode(b, p, mode)
			if e != nil {
				return
			}
			if e = kinds.ProveLimits(nil, out, b, repz3.Limits{MaxDepth: 4}); e != nil {
				t.Fatal(e)
			}
		}
	})
}

func TestDeclaredZeroWidthArrayWorkBound(t *testing.T) {
	w := writer{}
	w.vbr(1000000, 6)
	r := reader{data: w.b, end: w.n}
	a := abbreviation{{literal: true, value: 1}, {encoding: 3}, {encoding: 1, width: 0}}
	r.record(a, false)
	if r.err == nil || r.values > 1 {
		t.Fatal("zero-width array expanded before declared work bound")
	}
}
