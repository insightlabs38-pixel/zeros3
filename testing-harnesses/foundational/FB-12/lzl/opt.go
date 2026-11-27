package lzl

// ModeOpt: a bounded optimal parse in the style of LZMA's normal mode. From
// the current position, a forward dynamic program prices every literal, short
// rep, rep and match (every length of every match pair) with the model frozen
// at the block start, tracking the state and rep distances along each path.
// The block ends where all paths meet (no op crosses the position), when a
// match of nice length is found, or after optMax positions. The cheapest path
// is then emitted with the live model.

const (
	ModeOpt = 2
	optMax  = 2048
	inf     = ^uint32(0)
)

type optNode struct {
	price uint32
	prev  int32
	kind  uint8
	l     int32
	d     int32
	state uint8
	reps  [numReps]uint32
}

type lenPrices struct {
	match, rep [posStates][maxMatch + 1]uint32
	left       int
}

func (z *Encoder) refreshLenPrices() {
	lp := z.lp
	for ps := range posStates {
		for l := minMatch; l <= maxMatch; l++ {
			lp.match[ps][l] = priceLen(&z.m.matchLen, uint32(l-minMatch), ps)
			lp.rep[ps][l] = priceLen(&z.m.repLen, uint32(l-minMatch), ps)
		}
	}
	lp.left = 256
}

func nextSR(state int, reps [numReps]uint32, kind, d int) (int, [numReps]uint32) {
	switch kind {
	case 0:
		return stateLit(state), reps
	case 1:
		return stateMatch(state), [numReps]uint32{uint32(d - 1), reps[0], reps[1], reps[2]}
	case 2:
		v := reps[d]
		for k := d; k > 0; k-- {
			reps[k] = reps[k-1]
		}
		reps[0] = v
		return stateRep(state), reps
	}
	return stateShortRep(state), reps
}

type op struct {
	kind uint8
	l, d int32
}

func (z *Encoder) encodeOpt(src []byte) {
	n, nice := len(src), z.P.Nice
	f := &z.f
	if z.lp == nil {
		z.lp = new(lenPrices)
	}
	if cap(z.nodes) < optMax+maxMatch+2 {
		z.nodes = make([]optNode, optMax+maxMatch+2)
	}
	nodes := z.nodes[:optMax+maxMatch+2]
	var ops []op
	have := false
	for i := 0; i < n; {
		if z.lp.left <= 0 {
			z.refreshLenPrices()
		}
		if !have {
			z.cur = f.find(z.cur)
		}
		have = false
		// a long rep or match at the block start is taken at once
		repLen, repIdx := z.repLens(src, i)
		mainLen, mainDist := 0, 0
		if k := len(z.cur); k > 0 {
			mainLen, mainDist = int(z.cur[k-1].l), int(z.cur[k-1].d)
		}
		if repLen >= nice {
			z.emitRep(i, repIdx, repLen)
			z.lp.left--
			f.skip(repLen - 1)
			i += repLen
			continue
		}
		if mainLen >= nice {
			z.emitMatch(i, mainLen, mainDist)
			z.lp.left--
			f.skip(mainLen - 1)
			i += mainLen
			continue
		}
		if n-i <= 2 || (mainLen < 2 && repLen < 2) {
			z.literalOrShortRep(src, i)
			z.lp.left--
			i++
			continue
		}
		// forward DP over [i, i+end)
		nodes[0] = optNode{price: 0, prev: -1, state: uint8(z.state), reps: z.reps}
		lenEnd := 0
		end := -1
		for p := 0; ; p++ {
			if p > 0 {
				if p == lenEnd || p >= optMax {
					end = lenEnd
					if p < lenEnd {
						f.skip(lenEnd - p)
					}
					break
				}
				z.cur = f.find(z.cur)
				if k := len(z.cur); k > 0 && int(z.cur[k-1].l) >= nice {
					end = p // stop here; the long match starts the next block
					have = true
					break
				}
			}
			nd := &nodes[p]
			pos := i + p
			st, reps := int(nd.state), nd.reps
			base := nd.price
			ps := pos & (posStates - 1)
			relax := func(l int, price uint32, kind uint8, d int) {
				q := p + l
				for lenEnd < q {
					lenEnd++
					nodes[lenEnd].price = inf
				}
				if price < nodes[q].price {
					s2, r2 := nextSR(st, reps, int(kind), d)
					nodes[q] = optNode{price: price, prev: int32(p), kind: kind, l: int32(l), d: int32(d), state: uint8(s2), reps: r2}
				}
			}
			// literal
			relax(1, base+z.priceLitAt(src, pos, st, reps[0]), 0, 0)
			if pos > int(reps[0]) && src[pos] == src[pos-int(reps[0])-1] {
				relax(1, base+z.priceShortRep(pos, st), 3, 0)
			}
			avail := min(maxMatch, n-pos)
			if avail < minMatch {
				continue
			}
			isM := base + priceBit(z.m.isMatch[st][ps], 1)
			repBase := isM + priceBit(z.m.isRep[st], 1)
			for r := range numReps {
				d := int(reps[r]) + 1
				if d > pos {
					continue
				}
				lr := matchLen(src, pos-d, pos, avail)
				if lr < minMatch {
					continue
				}
				var c uint32
				if r == 0 {
					c = repBase + priceBit(z.m.isRepG0[st], 0) + priceBit(z.m.isRep0Long[st][ps], 1)
				} else {
					c = repBase + priceBit(z.m.isRepG0[st], 1)
					if r == 1 {
						c += priceBit(z.m.isRepG1[st], 0)
					} else {
						c += priceBit(z.m.isRepG1[st], 1) + priceBit(z.m.isRepG2[st], uint32(r-2))
					}
				}
				for l := minMatch; l <= lr; l++ {
					relax(l, c+z.lp.rep[ps][l], 2, r)
				}
			}
			mBase := isM + priceBit(z.m.isRep[st], 0)
			from := 3
			for _, pr := range z.cur {
				pl, pd := int(pr.l), int(pr.d)
				var dp [lenStates]uint32
				for ls := range lenStates {
					dp[ls] = priceDist(&z.m, uint32(pd-1), ls)
				}
				for l := from; l <= pl; l++ {
					relax(l, mBase+z.lp.match[ps][l]+dp[lenState(l)], 1, pd)
				}
				from = pl + 1
			}
		}
		// backtrack and emit
		ops = ops[:0]
		for q := end; q > 0; q = int(nodes[q].prev) {
			nd := &nodes[q]
			ops = append(ops, op{nd.kind, nd.l, nd.d})
		}
		for k := len(ops) - 1; k >= 0; k-- {
			o := ops[k]
			switch o.kind {
			case 0:
				z.emitLit(src, i)
			case 1:
				z.emitMatch(i, int(o.l), int(o.d))
			case 2:
				z.emitRep(i, int(o.d), int(o.l))
			case 3:
				z.emitShortRep(i)
			}
			z.lp.left--
			i += int(o.l)
		}
	}
}
