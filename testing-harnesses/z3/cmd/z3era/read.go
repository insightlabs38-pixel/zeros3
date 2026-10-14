package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"time"

	"zeros3-z3/eraez3"
	"zeros3-z3/lab"
)

// cmdRead measures seeking one component in the three storage shapes.
func cmdRead() error {
	f := load("sepolia-00000-8e3e7dc9.erae")
	rep, st, err := eraez3.Build(bytes.NewReader(f), int64(len(f)), eraez3.DefaultLimits(), true)
	if err != nil {
		return err
	}
	ents, _ := eraez3.Parse(bytes.NewReader(f), int64(len(f)), eraez3.DefaultLimits())
	fmt.Printf("sepolia-00000: %d bytes, %d blocks\n\n", len(f), st.Blocks)

	// raw object: the original component index gives an O(1) offset.
	var hdrs []eraez3.Entry
	for _, e := range ents {
		if e.Type == eraez3.TypeHeader {
			hdrs = append(hdrs, e)
		}
	}
	// plain representation: per-chunk item counts locate the chunk.
	in := &lab.Input{Family: "r", Format: "erae", Revision: "r", Src: lab.BytesSource("f", f), Opts: lab.DefaultOptions()}
	var plan *lab.Plan
	for _, r := range lab.EraReps() {
		if r.Name == "era-plain" {
			plan, err = r.Analyze(in)
			if err != nil {
				return err
			}
		}
	}
	ac := lab.NewAcct(35, true)
	acc, err := ac.Add(plan)
	if err != nil {
		return err
	}
	// header stream: item offsets (RLP) and the CDC chunk boundaries of the plaintext
	hs := rep.Streams[eraez3.TypeHeader]
	var itemOff []int
	for n := 0; n < len(hs); {
		l, ok := rlpLen(hs[n:])
		if !ok {
			return fmt.Errorf("header stream is not RLP-delimited")
		}
		itemOff = append(itemOff, n)
		n += l
	}
	itemOff = append(itemOff, len(hs))
	var chunkEnd []int
	{
		pos := 0
		for _, pc := range plan.Pieces {
			if pc.Comp != nil && pc.Comp.Class == "era-header" {
				pos += int(pc.Comp.Size)
				chunkEnd = append(chunkEnd, pos)
			}
		}
	}
	const K = 128 // checkpoint spacing in items
	fmt.Printf("header stream: %d items, %d CDC chunks (avg %.0f B plaintext); seek table = one varint per %d items (%d entries)\n\n", len(itemOff)-1, len(chunkEnd), float64(len(hs))/float64(len(chunkEnd)), K, (len(itemOff)-1+K-1)/K)
	fmt.Printf("| read block header k (200 random k) | bytes fetched | notes |\n|---|--:|---|\n")
	var rawBytes, plainBytes int64
	for i := 0; i < 200; i++ {
		k := (i*2749 + 5) % len(hdrs)
		rawBytes += int64(hdrs[k].Len) + 8 + 16 // entry + the 2 index words
		lo, hi := itemOff[k/K*K], itemOff[k+1]
		var prev int
		for _, ce := range chunkEnd {
			if ce > lo && prev < hi {
				plainBytes += int64(ce - prev)
			}
			prev = ce
		}
	}
	fmt.Printf("| raw object + original component index (range GET) | %d | one entry (+16 B of index) |\n", rawBytes/200)
	fmt.Printf("| era-plain: chunks covering checkpoint..item, plaintext | %d | then walk <= %d RLP headers and re-frame the entry; stored chunks are pack-compressed (~1/4 on headers) |\n", plainBytes/200, K)

	t0 := time.Now()
	var buf bytes.Buffer
	t0 = time.Now()
	const reps = 5
	for i := 0; i < reps; i++ {
		buf.Reset()
		if err := plan.Rebuild(&buf, ac.Get); err != nil {
			return err
		}
	}
	dt := time.Since(t0) / reps
	if sha256.Sum256(buf.Bytes()) != sha256.Sum256(f) {
		return fmt.Errorf("rebuild differs")
	}
	fmt.Printf("\nsequential rebuild of the whole file from components: %v (%.0f MiB/s; includes the reference Snappy encoder over %.1f MiB of plaintext)\n", dt.Round(time.Millisecond), float64(len(f))/(1<<20)/dt.Seconds(), float64(st.PlainBytes)/(1<<20))
	fmt.Printf("stored for the file: new component bytes %d, recipe %d B, packed estimate %d B (file %d B)\n", acc.NewBytes, acc.PlanMetaBytes, acc.PackedNew, len(f))
	return nil
}

func avg(v []int64) float64 {
	var s int64
	for _, x := range v {
		s += x
	}
	return float64(s) / float64(len(v))
}

// rlpLen returns the length of the RLP item at b (list or string).
func rlpLen(b []byte) (int, bool) {
	if len(b) == 0 {
		return 0, false
	}
	c := b[0]
	switch {
	case c < 0x80:
		return 1, true
	case c <= 0xb7:
		return 1 + int(c-0x80), true
	case c <= 0xbf:
		ll := int(c - 0xb7)
		return lenWith(b, 1, ll)
	case c <= 0xf7:
		return 1 + int(c-0xc0), true
	}
	return lenWith(b, 1, int(c-0xf7))
}

func lenWith(b []byte, h, ll int) (int, bool) {
	if len(b) < h+ll {
		return 0, false
	}
	n := 0
	for i := 0; i < ll; i++ {
		n = n<<8 | int(b[h+i])
	}
	return h + ll + n, true
}
