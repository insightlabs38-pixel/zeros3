package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
)

func df1ReferenceUV(r *repWireReader, max uint64) uint64 {
	if r.err != nil {
		return 0
	}
	value, n := binary.Uvarint(r.b[r.pos:])
	if n <= 0 || n != len(binary.AppendUvarint(nil, value)) {
		r.fail("varint at %d", r.pos)
		return 0
	}
	if value > max {
		r.fail("value %d above %d at %d", value, max, r.pos)
		return 0
	}
	r.pos += n
	return value
}

func df1CheckUV(t *testing.T, data []byte, pos int, max uint64, prior error) {
	t.Helper()
	got := repWireReader{b: data, pos: pos, err: prior}
	want := got
	gotValue := got.uv(max)
	wantValue := df1ReferenceUV(&want, max)
	if gotValue != wantValue || got.pos != want.pos || fmt.Sprint(got.err) != fmt.Sprint(want.err) {
		t.Fatalf("%x at %d limit %d: got (%d,%d,%v), want (%d,%d,%v)",
			data, pos, max, gotValue, got.pos, got.err, wantValue, want.pos, want.err)
	}
}

func TestDF1CanonicalVarints(t *testing.T) {
	all := ^uint64(0)
	var data [2]byte
	df1CheckUV(t, nil, 0, all, nil)
	for i := range 256 {
		data[0] = byte(i)
		df1CheckUV(t, data[:1], 0, all, nil)
		for j := range 256 {
			data[1] = byte(j)
			df1CheckUV(t, data[:], 0, all, nil)
			df1CheckUV(t, data[:], 0, 63, nil)
		}
	}
	values := []uint64{0, 1, all - 1, all}
	for i := 7; i < 64; i += 7 {
		start := uint64(1) << i
		values = append(values, start-1, start, start+1)
	}
	for _, value := range values {
		wire := binary.AppendUvarint([]byte{42}, value)
		for _, max := range []uint64{value - 1, value, value + 1, all} {
			df1CheckUV(t, wire, 1, max, nil)
			df1CheckUV(t, append(wire, 91), 1, max, nil)
			df1CheckUV(t, wire[:len(wire)-1], 1, max, nil)
		}
		for len(wire) <= 11 {
			wire[len(wire)-1] |= 0x80
			wire = append(wire, 0)
			df1CheckUV(t, wire, 1, all, nil)
		}
	}
	overflow := []byte{255, 255, 255, 255, 255, 255, 255, 255, 255, 0}
	for i := range 256 {
		overflow[9] = byte(i)
		df1CheckUV(t, overflow, 0, all, nil)
		df1CheckUV(t, append(overflow, 4), 0, 1, nil)
	}
	df1CheckUV(t, []byte{9}, 0, all, errors.New("prior failure"))
	rng := rand.New(rand.NewPCG(23, 613))
	var wire [17]byte
	for range 40000 {
		for i := range wire {
			wire[i] = byte(rng.Uint32())
		}
		size := rng.IntN(len(wire) + 1)
		df1CheckUV(t, wire[:size], rng.IntN(size+1), rng.Uint64(), nil)
	}
}

func TestDF1CanonicalVarintNoAllocation(t *testing.T) {
	wire := []byte{0, 127, 128, 1, 0xff, 0xff, 0xff, 0xff, 0x0f}
	n := testing.AllocsPerRun(1000, func() {
		r := repWireReader{b: wire}
		for range 4 {
			r.uv(^uint64(0))
		}
		if r.err != nil || r.pos != len(wire) {
			panic("benchmark varints failed")
		}
	})
	if n != 0 {
		t.Fatalf("valid wire used %.2f allocations, expected zero", n)
	}
}

func BenchmarkDF1CanonicalVarint(b *testing.B) {
	wire := []byte{0, 0, 1, 15, 127, 128, 1, 255, 255, 255, 255, 15}
	b.ReportAllocs()
	b.SetBytes(int64(len(wire)))
	for b.Loop() {
		r := repWireReader{b: wire}
		for range 7 {
			r.uv(^uint64(0))
		}
	}
}

func FuzzDF1CanonicalVarintCompatibility(f *testing.F) {
	f.Add([]byte{0}, uint64(0), uint8(0))
	f.Add([]byte{128, 0}, ^uint64(0), uint8(0))
	f.Add([]byte{255, 255, 255, 255, 255, 255, 255, 255, 255, 2}, ^uint64(0), uint8(0))
	f.Fuzz(func(t *testing.T, wire []byte, max uint64, offset uint8) {
		if len(wire) > 128 {
			t.Skip()
		}
		df1CheckUV(t, wire, int(offset)%(len(wire)+1), max, nil)
	})
}
