package main

import (
	"bytes"
	"encoding/binary"
	"math/bits"
	"runtime"
	"sort"
	"sync"

	"zeros3-z3/st"
	"zeros3-z3/tdelta"
)

const refOverhead = 36 // flag + 35 B reference to the base tensor, charged per delta-coded tensor

// pairResult is everything measured about target B against base A.
type pairResult struct {
	a, b *ckpt

	// identity
	tensorsA, tensorsB                            int
	bytesB                                        int64
	sameNameIdentical, sameNameIdenticalBytes     int64 // same name, dtype, shape and bytes
	alignedChanged, alignedChangedBytes           int64 // same name/dtype/shape, bytes differ
	renamedExact, renamedExactBytes               int64 // new name, bytes equal to some tensor of A
	added, addedBytes                             int64
	removed, removedBytes                         int64
	typeShapeChanged, typeShapeChangedBytes       int64 // same name, different dtype or shape
	renameCandidates, renameCandidatesBytes       int64 // added+removed pairs with equal dtype and shape (not byte-identical)
	headerIdentical, jsonOrderSame, dataOrderSame bool
	offsetsDiffer                                 int

	// per dtype bit statistics over aligned tensors (identical ones included)
	stats map[string]*tdelta.Stats
	// per dtype: aligned changed tensor payload bytes and cost per codec
	dBytes         map[string]int64
	dCost          map[string]map[tdelta.Codec]int64
	dBest          map[string]int64 // sum over tensors of min(codec costs, replace)+ref
	dBestNoReplace map[string]int64
	dTensors       map[string]int64

	// per class: bytes of B, identical tensor bytes, equal elements, best delta cost
	class map[string]*classAgg

	xorEntropyBits, xorBytes float64 // order-0 entropy of the XOR stream (byte planes of float tensors pooled per plane) weighted
	changedList              []*tensorDelta
}

type classAgg struct {
	tensors, identical             int64
	bytes, identicalBytes          int64
	elems, equalElems              int64
	deltaBytes, targetChangedBytes int64
}

type tensorDelta struct {
	name, dtype, class string
	n                  int
	cost               map[tdelta.Codec]int
	best               int
	replace            int
}

func parallel(n int, f func(i int)) {
	var wg sync.WaitGroup
	w := runtime.NumCPU()
	ch := make(chan int)
	for k := 0; k < w; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				f(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		ch <- i
	}
	close(ch)
	wg.Wait()
}

type pairOpts struct {
	delta bool // compute codec costs (expensive)
}

func analyzePair(a, b *ckpt, o pairOpts) *pairResult {
	r := &pairResult{a: a, b: b, tensorsA: len(a.f.Tensors), tensorsB: len(b.f.Tensors),
		stats: map[string]*tdelta.Stats{}, dBytes: map[string]int64{}, dCost: map[string]map[tdelta.Codec]int64{},
		dBest: map[string]int64{}, dBestNoReplace: map[string]int64{}, dTensors: map[string]int64{}, class: map[string]*classAgg{}}
	r.headerIdentical = bytes.Equal(a.f.Header, b.f.Header)
	r.jsonOrderSame = equalStrings(a.f.JSONOrder, b.f.JSONOrder)
	var an, bn []string
	for i := range a.f.Tensors {
		an = append(an, a.f.Tensors[i].Name)
	}
	for i := range b.f.Tensors {
		bn = append(bn, b.f.Tensors[i].Name)
	}
	r.dataOrderSame = equalStrings(an, bn)
	ah, bh := a.hashes(), b.hashes()
	byHash := map[[32]byte]string{}
	for n, h := range ah {
		byHash[h] = n
	}
	type pend struct {
		ta, tb *st.Tensor
	}
	var changed []pend
	addedKey := map[string][]*st.Tensor{}
	for i := range b.f.Tensors {
		tb := &b.f.Tensors[i]
		r.bytesB += tb.Size()
		cls := tensorClass(tb.Name)
		ca := r.class[cls]
		if ca == nil {
			ca = &classAgg{}
			r.class[cls] = ca
		}
		ca.tensors++
		ca.bytes += tb.Size()
		ca.elems += tb.Elems
		ta, ok := a.f.Lookup(tb.Name)
		switch {
		case !ok:
			if _, same := byHash[bh[tb.Name]]; same && tb.Size() > 0 {
				r.renamedExact++
				r.renamedExactBytes += tb.Size()
			} else {
				r.added++
				r.addedBytes += tb.Size()
				k := tb.DType + shapeKey(tb.Shape)
				addedKey[k] = append(addedKey[k], tb)
			}
		case ta.DType != tb.DType || !equalInts(ta.Shape, tb.Shape):
			r.typeShapeChanged++
			r.typeShapeChangedBytes += tb.Size()
		default:
			if ta.Begin != tb.Begin {
				r.offsetsDiffer++
			}
			if ah[ta.Name] == bh[tb.Name] {
				r.sameNameIdentical++
				r.sameNameIdenticalBytes += tb.Size()
				ca.identical++
				ca.identicalBytes += tb.Size()
				ca.equalElems += tb.Elems
				s := r.stats[tb.DType]
				if s == nil {
					s = &tdelta.Stats{}
					r.stats[tb.DType] = s
				}
				s.Bytes += tb.Size()
				s.Elems += tb.Elems
				s.EqualBytes += tb.Size()
				s.EqualElems += tb.Elems
				s.Blocks4K += (tb.Size() + 4095) / 4096
			} else {
				r.alignedChanged++
				r.alignedChangedBytes += tb.Size()
				changed = append(changed, pend{ta, tb})
			}
		}
	}
	for i := range a.f.Tensors {
		ta := &a.f.Tensors[i]
		if _, ok := b.f.Lookup(ta.Name); !ok {
			if _, moved := bytesInB(b, ah[ta.Name]); !moved {
				r.removed++
				r.removedBytes += ta.Size()
			}
		}
	}
	// rename candidates: added tensor vs removed tensor with equal dtype+shape
	for i := range a.f.Tensors {
		ta := &a.f.Tensors[i]
		if _, ok := b.f.Lookup(ta.Name); ok {
			continue
		}
		k := ta.DType + shapeKey(ta.Shape)
		if l := addedKey[k]; len(l) > 0 {
			r.renameCandidates++
			r.renameCandidatesBytes += l[0].Size()
			addedKey[k] = l[1:]
		}
	}
	// bit statistics and deltas for the changed aligned tensors, in parallel
	type res struct {
		stats tdelta.Stats
		td    *tensorDelta
		xe    float64
	}
	out := make([]res, len(changed))
	parallel(len(changed), func(i int) {
		ta, tb := changed[i].ta, changed[i].tb
		pa, pb := a.payload(ta), b.payload(tb)
		w := tb.Bits / 8
		if tb.Bits%8 != 0 {
			w = 1
		}
		out[i].stats = tdelta.Compare(tb.DType, pa, pb, w, tb.Shape)
		x := make([]byte, len(pb))
		for j := range x {
			x[j] = pa[j] ^ pb[j]
		}
		out[i].xe = tdelta.Entropy(x) * float64(len(x))
		if o.delta {
			td := &tensorDelta{name: tb.Name, dtype: tb.DType, class: tensorClass(tb.Name), n: len(pb), cost: map[tdelta.Codec]int{}}
			best := 1 << 62
			for c := tdelta.XOR; c < tdelta.NumCodecs; c++ {
				if c == tdelta.FloatSplit && tb.DType != "F32" && tb.DType != "F16" && tb.DType != "BF16" {
					continue
				}
				p, err := tdelta.EncodeFor(c, tb.DType, pa, pb, w)
				if err != nil {
					continue
				}
				td.cost[c] = len(p)
				if c == tdelta.Replace || c == tdelta.ReplacePlane {
					if td.replace == 0 || len(p) < td.replace {
						td.replace = len(p)
					}
				} else if len(p) < best {
					best = len(p)
				}
			}
			td.best = best
			out[i].td = td
		}
	})
	for i, c := range changed {
		tb := c.tb
		s := r.stats[tb.DType]
		if s == nil {
			s = &tdelta.Stats{}
			r.stats[tb.DType] = s
		}
		addStats(s, &out[i].stats)
		r.xorEntropyBits += out[i].xe
		r.xorBytes += float64(tb.Size())
		ca := r.class[tensorClass(tb.Name)]
		ca.equalElems += out[i].stats.EqualElems
		ca.targetChangedBytes += tb.Size()
		if td := out[i].td; td != nil {
			r.changedList = append(r.changedList, td)
			r.dTensors[tb.DType]++
			r.dBytes[tb.DType] += int64(td.n)
			if r.dCost[tb.DType] == nil {
				r.dCost[tb.DType] = map[tdelta.Codec]int64{}
			}
			for c, v := range td.cost {
				r.dCost[tb.DType][c] += int64(v)
			}
			bestAny := min(td.best, td.replace, td.n)
			r.dBest[tb.DType] += int64(bestAny + refOverhead)
			r.dBestNoReplace[tb.DType] += int64(min(td.best, td.n) + refOverhead)
			ca.deltaBytes += int64(bestAny + refOverhead)
		}
	}
	sort.Slice(r.changedList, func(i, j int) bool { return r.changedList[i].name < r.changedList[j].name })
	return r
}

func bytesInB(b *ckpt, h [32]byte) (string, bool) {
	for n, hb := range b.hashes() {
		if hb == h {
			return n, true
		}
	}
	return "", false
}

func addStats(dst, s *tdelta.Stats) {
	dst.Bytes += s.Bytes
	dst.Elems += s.Elems
	dst.EqualBytes += s.EqualBytes
	dst.EqualElems += s.EqualElems
	dst.XorPopcount += s.XorPopcount
	dst.SignChanged += s.SignChanged
	dst.ExpChanged += s.ExpChanged
	dst.MantChanged += s.MantChanged
	dst.ExpBitsFlipped += s.ExpBitsFlipped
	dst.MantBitsFlipped += s.MantBitsFlipped
	dst.SignBitsFlipped += s.SignBitsFlipped
	dst.BlockChanged4K += s.BlockChanged4K
	dst.Blocks4K += s.Blocks4K
	dst.AdjChangedPairs += s.AdjChangedPairs
	dst.ChangedElems += s.ChangedElems
	for i := range dst.DistLog2 {
		dst.DistLog2[i] += s.DistLog2[i]
	}
}

func shapeKey(s []int64) string {
	b := make([]byte, 0, 8*len(s))
	for _, d := range s {
		b = binary.LittleEndian.AppendUint64(b, uint64(d))
	}
	return string(b)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// medianLog2 returns the median of log2(1+|ulp distance|) over changed elements.
func medianLog2(h [34]int64) int {
	var tot int64
	for _, v := range h {
		tot += v
	}
	var acc int64
	for i, v := range h {
		acc += v
		if acc*2 >= tot {
			return i
		}
	}
	return 0
}

var _ = bits.Len
