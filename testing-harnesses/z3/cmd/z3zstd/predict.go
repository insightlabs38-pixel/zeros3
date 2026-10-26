package main

import (
	"bytes"
	"flag"
	"fmt"
	"math"
	"os"

	"zeros3-z3/zstdz3"
)

// hist is a histogram with Shannon entropy.
type hist map[int]int64

func (h hist) bits() (total int64, bits float64) {
	for _, c := range h {
		total += c
	}
	for _, c := range h {
		bits -= float64(c) * math.Log2(float64(c)/float64(total))
	}
	return
}

func lce(a, b []byte) int {
	n := min(len(a), len(b))
	i := 0
	for i+8 <= n {
		x := uint64(a[i]) | uint64(a[i+1])<<8 | uint64(a[i+2])<<16 | uint64(a[i+3])<<24 | uint64(a[i+4])<<32 | uint64(a[i+5])<<40 | uint64(a[i+6])<<48 | uint64(a[i+7])<<56
		y := uint64(b[i]) | uint64(b[i+1])<<8 | uint64(b[i+2])<<16 | uint64(b[i+3])<<24 | uint64(b[i+4])<<32 | uint64(b[i+5])<<40 | uint64(b[i+6])<<48 | uint64(b[i+7])<<56
		if x != y {
			break
		}
		i += 8
	}
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

func log2ceil(v uint32) int {
	n := 0
	for v > 1 {
		v >>= 1
		n++
	}
	return n
}

// cmdPredict characterises how much of the sequence decisions the plaintext already
// determines, without emulating any compressor: the match length against the longest
// extension at the chosen offset, and the offset against the rank of the chosen source
// among all earlier positions that match at least as far, nearest first.
func cmdPredict(args []string) {
	fs := flag.NewFlagSet("predict", flag.ExitOnError)
	in := fs.String("in", "", "zstd file")
	maxChain := fs.Int("chain", 512, "chain steps per sequence")
	fs.Parse(args)
	zst, err := os.ReadFile(*in)
	must(err)
	var plain, units bytes.Buffer
	res, err := zstdz3.Parse(bytes.NewReader(zst), &plain, &units, zstdz3.Options{NoSeqModel: true})
	must(err)
	P := plain.Bytes()
	const hbits = 22
	head := make([]int32, 1<<hbits)
	for i := range head {
		head[i] = -1
	}
	prev := make([]int32, len(P))
	hash := func(p int) uint32 {
		v := uint32(P[p]) | uint32(P[p+1])<<8 | uint32(P[p+2])<<16 | uint32(P[p+3])<<24
		return (v * 2654435761) >> (32 - hbits)
	}
	ins := 0
	insert := func(upto int) {
		for ; ins < upto && ins+4 <= len(P); ins++ {
			h := hash(ins)
			prev[ins] = head[h]
			head[h] = int32(ins)
		}
	}
	var nseq, nrep, nshort int64
	llH, mlResH, rankH, extraBits := hist{}, hist{}, hist{}, 0.0
	bestH, clsH, nearH := hist{}, hist{}, hist{}
	var mlMax, far int64
	var llExtra, mlExtra, offExtra float64
	code := func(v uint32) (int, float64) { // zstd-like bucket: log2 class + extra bits
		if v < 16 {
			return int(v), 0
		}
		l := log2ceil(v)
		return 100 + l, float64(l)
	}
	err = zstdz3.Sequences(res.Skeleton, bytes.NewReader(units.Bytes()), func(fi, bi int, plainOff int64, seqs []zstdz3.Seq) error {
		bend := plainOff + int64(res.Skeleton.Frames[fi].Blocks[bi].DecLen)
		pos := plainOff
		for _, s := range seqs {
			nseq++
			c, e := code(s.LL)
			llH[c]++
			llExtra += e
			q := pos + int64(s.LL)
			pos = q + int64(s.ML)
			src := q - int64(s.Offset)
			maxExt := lce(P[src:], P[q:bend])
			r := int(int64(maxExt) - int64(s.ML))
			if r == 0 {
				mlMax++
			}
			c, e = code(uint32(max(r, 0)))
			mlResH[c]++
			mlExtra += e
			if s.OfVal <= 3 {
				nrep++
				rankH[-1]++
				continue
			}
			if s.ML < 4 {
				nshort++
				c, e = code(s.Offset)
				rankH[1000+c]++
				offExtra += e
				continue
			}
			insert(int(q))
			rank, rankBest, rankCls, rankNear := 0, 0, 0, 0
			steps := 0
			found := false
			cls := func(l int) int {
				switch {
				case l >= 16:
					return 0
				case l >= 8:
					return 1
				}
				return 2
			}
			mc := cls(int(s.ML))
			for p := head[hash(int(q))]; p >= 0 && steps < *maxChain; p, steps = prev[p], steps+1 {
				l := lce(P[p:], P[q:bend])
				if int64(p) == src {
					found = true
				} else if int64(p) > src && l >= int(s.ML) {
					rank++
				}
				if int64(p) > src && l >= int(s.ML) || l > int(s.ML) {
					rankBest++
				}
				if int64(p) != src && (cls(l) < mc || cls(l) == mc && int64(p) > src) {
					rankCls++
				}
				if int64(p) > src && l >= 4 {
					rankNear++
				}
				if int64(p) < src && !found {
					break
				}
			}
			if found {
				bestH[min(rankBest, 40)]++
				clsH[min(rankCls, 40)]++
				nearH[min(rankNear, 40)]++
			}
			if !found {
				far++
				c, e = code(s.Offset)
				rankH[2000+c]++
				offExtra += e
				continue
			}
			if rank >= 16 {
				c, e = code(uint32(rank))
				rankH[3000+c]++
				offExtra += e
				continue
			}
			rankH[rank]++
		}
		return nil
	})
	must(err)
	_ = extraBits
	nl, bl := llH.bits()
	_, bm := mlResH.bits()
	_, br := rankH.bits()
	tot := (bl + llExtra + bm + mlExtra + br + offExtra) / 8
	fmt.Printf("%s: %d sequences, %d via repeat codes, %d short (ml<4), %d source beyond %d chain steps\n", *in, nseq, nrep, nshort, far, *maxChain)
	fmt.Printf("match length equals the longest extension: %.1f%%\n", 100*float64(mlMax)/float64(nseq))
	fmt.Printf("rank histogram (nearest equally-long source = 0):")
	for r := 0; r < 8; r++ {
		fmt.Printf(" %d:%.1f%%", r, 100*float64(rankH[r])/float64(nseq))
	}
	fmt.Printf(" rep:%.1f%%\n", 100*float64(rankH[-1])/float64(nseq))
	fmt.Printf("best-first rank (LCE desc, nearest first), chain %d:", *maxChain)
	for r := 0; r < 8; r++ {
		fmt.Printf(" %d:%.1f%%", r, 100*float64(bestH[r])/float64(nseq))
	}
	_, bb := bestH.bits()
	fmt.Printf(" (order-0 %.2f bits/seq over ranks)\n", bb/float64(nseq))
	for _, o := range []struct {
		n string
		h hist
	}{{"class(16/8/4)+nearest", clsH}, {"nearest of all >=4", nearH}} {
		_, bo := o.h.bits()
		fmt.Printf("%s:", o.n)
		for r := 0; r < 6; r++ {
			fmt.Printf(" %d:%.1f%%", r, 100*float64(o.h[r])/float64(nseq))
		}
		fmt.Printf(" (order-0 %.2f bits/seq)\n", bo/float64(nseq))
	}
	fmt.Printf("order-0 entropy bytes: LL %.0f+%.0f  ML-residual %.0f+%.0f  offset-rank %.0f+%.0f  => %.1f MB (%.2f B/seq); explicit unit stream %.1f MB (%.2f B/seq); sequences of the original stream (incl. literals) %.1f MB\n",
		bl/8, llExtra/8, bm/8, mlExtra/8, br/8, offExtra/8, tot/1e6, tot/float64(nl), float64(units.Len())/1e6, float64(units.Len())/float64(nl), float64(res.Stats.StreamBytes[zstdz3.BlockCompressed])/1e6)
}
