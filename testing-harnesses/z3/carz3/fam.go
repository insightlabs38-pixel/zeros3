package carz3

// Rev is one revision of a CAR family: the exact archive bytes plus the
// ground truth the generator knows.
type Rev struct {
	Label string
	Data  []byte
	// Changed is how the generator mutated the parent: the number of blocks
	// whose payload is new to the family (0 for reorder/root/index/wrapper edits).
	NewBlocks int
}

// Family is a chain r0..rN; revision i mutates (or re-serialises) revision i-1.
type Family struct {
	Name string
	Note string
	Revs []Rev
}

// Scale sizes a family set.
type Scale struct {
	Name     string
	N        int // sections in the base archive
	Min, Max int // leaf payload bytes
	Fan      int
}

// Scales used by the laboratory. Big blocks resemble file chunks (IPFS UnixFS
// raw leaves); small blocks resemble IPLD nodes, where per-block metadata is
// the dominant cost.
var (
	BigBlocks   = Scale{"big", 120, 16 << 10, 192 << 10, 8}
	SmallBlocks = Scale{"small", 12000, 120, 900, 4}
	TinyBlocks  = Scale{"tiny", 60, 200, 3000, 6}
)

func rootOf(bs []Block) []byte { return bs[len(bs)-1].CID }

func extra(seed uint64, n int, sc Scale) []Block {
	bs, _ := Gen(GenSpec{Seed: seed, N: n, Min: sc.Min, Max: sc.Max, Fan: 1 << 20})
	return bs[:n]
}

func revBlocks(label string, newBlocks int, hdr []byte, bs []Block) Rev {
	return Rev{Label: label, Data: V1With(hdr, bs), NewBlocks: newBlocks}
}

func reversed(bs []Block) []Block {
	out := make([]Block, len(bs))
	for i, b := range bs {
		out[len(bs)-1-i] = b
	}
	return out
}

// insertAt returns bs with add inserted at pseudo-random positions.
func insertAt(seed uint64, bs, add []Block) []Block {
	out := append([]Block(nil), bs...)
	for k, b := range add {
		pos := int((seed*2654435761 + uint64(k)*40503) % uint64(len(out)+1))
		out = append(out[:pos], append([]Block{b}, out[pos:]...)...)
	}
	return out
}

// Families returns the Z3-14 deterministic corpus for one scale. Names are
// prefixed with the scale when it is not "big".
func Families(sc Scale) []Family {
	base, root := Gen(GenSpec{Seed: 14, N: sc.N, Min: sc.Min, Max: sc.Max, Fan: sc.Fan})
	hdr := HeaderBody([][]byte{root}, true)
	name := func(s string) string {
		if sc.Name == "big" {
			return s
		}
		return sc.Name + "-" + s
	}
	nodeCID := base[sc.Fan].CID
	var fams []Family

	fams = append(fams, Family{Name: name("v1-reorder"), Note: "same blocks, different section order", Revs: []Rev{
		revBlocks("r0 base", sc.N, hdr, base),
		revBlocks("r1 shuffled", 0, hdr, Shuffled(1, base)),
		revBlocks("r2 reversed", 0, hdr, reversed(Shuffled(1, base))),
		revBlocks("r3 sorted-by-CID", 0, hdr, SortedByCID(base)),
	}})

	fams = append(fams, Family{Name: name("v1-roots"), Note: "same blocks, different roots array", Revs: []Rev{
		revBlocks("r0 roots=[R]", sc.N, hdr, base),
		revBlocks("r1 roots=[R,N]", 0, HeaderBody([][]byte{root, nodeCID}, true), base),
		revBlocks("r2 roots=[N,R]", 0, HeaderBody([][]byte{nodeCID, root}, true), base),
		revBlocks("r3 roots=[leaf]", 0, HeaderBody([][]byte{base[0].CID}, true), base),
		revBlocks("r4 roots=[] key order swapped", 0, HeaderBody(nil, false), base),
	}})

	a1, a4, a1b := extra(101, 1, sc), extra(102, 4, sc), extra(103, 1, sc)
	v1 := append(append([]Block(nil), base...), a1...)
	v2 := insertAt(7, v1, a4)
	v3 := append(append([]Block(nil), a1b...), v2...)
	fams = append(fams, Family{Name: name("v1-add"), Note: "new blocks appended / inserted / prepended", Revs: []Rev{
		revBlocks("r0 base", sc.N, hdr, base),
		revBlocks("r1 +1 appended", 1, hdr, v1),
		revBlocks("r2 +4 inserted", 4, hdr, v2),
		revBlocks("r3 +1 prepended", 1, hdr, v3),
	}})

	rm1 := append(append([]Block(nil), base[:3]...), base[4:]...)
	rm4 := append(append([]Block(nil), rm1[:10]...), rm1[11:20]...)
	rm4 = append(append(rm4, rm1[21:40]...), rm1[41:]...)
	fams = append(fams, Family{Name: name("v1-remove"), Note: "blocks removed (roots unchanged)", Revs: []Rev{
		revBlocks("r0 base", sc.N, hdr, base),
		revBlocks("r1 -1", 0, hdr, rm1),
		revBlocks("r2 -4 more", 0, hdr, rm4),
	}})

	u, uroot := Gen(GenSpec{Seed: 15, N: sc.N * 2, Min: sc.Min, Max: sc.Max, Fan: sc.Fan})
	uh := HeaderBody([][]byte{uroot}, true)
	n := sc.N
	fams = append(fams, Family{Name: name("v1-overlap"), Note: "independent archives with ~50% shared blocks", Revs: []Rev{
		revBlocks("r0 U[0:N]", n, uh, u[:n]),
		revBlocks("r1 U[N/2:3N/2]", n/2, uh, u[n/2:n+n/2]),
		revBlocks("r2 shuffled U[N/4:5N/4]", 0, uh, Shuffled(2, u[n/4:n+n/4])),
		revBlocks("r3 U[3N/2:2N] + U[0:N/4]", n/2, uh, append(append([]Block(nil), u[n+n/2:]...), u[:n/4]...)),
	}})

	dup1 := make([]Block, 0, len(base)+8)
	for i, b := range base {
		dup1 = append(dup1, b)
		if i%(len(base)/5+1) == 0 {
			dup1 = append(dup1, b)
		}
	}
	dup2 := append(append([]Block(nil), base...), base[:5]...)
	fams = append(fams, Family{Name: name("v1-dup"), Note: "repeated section entries (same CID, same payload)", Revs: []Rev{
		revBlocks("r0 base", sc.N, hdr, base),
		revBlocks("r1 5 adjacent dups", 0, hdr, dup1),
		revBlocks("r2 5 appended dups", 0, hdr, dup2),
	}})

	rootFor := func(bs []Block) []byte { return bs[len(bs)-1].CID }
	rc := func(codec, mh uint64, v0 bool) []Block { return Recode(base, codec, mh, v0) }
	b1, b2, b3, b4 := rc(CodecDagPB, MhSHA2_256, false), rc(CodecDagPB, MhSHA2_256, true), rc(CodecRaw, MhSHA2_512, false), rc(CodecRaw, MhSHA3_256, false)
	fams = append(fams, Family{Name: name("v1-recodec"), Note: "same payloads, different CID codec / version / hash", Revs: []Rev{
		revBlocks("r0 raw/sha2-256", sc.N, hdr, base),
		revBlocks("r1 dag-pb/sha2-256", 0, HeaderBody([][]byte{rootFor(b1)}, true), b1),
		revBlocks("r2 CIDv0", 0, HeaderBody([][]byte{rootFor(b2)}, true), b2),
		revBlocks("r3 raw/sha2-512", 0, HeaderBody([][]byte{rootFor(b3)}, true), b3),
		revBlocks("r4 raw/sha3-256", 0, HeaderBody([][]byte{rootFor(b4)}, true), b4),
	}})

	c1, c2 := len(base)*6/10, len(base)*8/10
	fams = append(fams, Family{Name: name("v1-append"), Note: "sequential growth: each revision extends the previous bytes", Revs: []Rev{
		revBlocks("r0 first 60%", c1, hdr, base[:c1]),
		revBlocks("r1 +20%", c2-c1, hdr, base[:c2]),
		revBlocks("r2 +20%", len(base)-c2, hdr, base),
	}})

	mix := make([]Block, 0, len(base))
	for i, b := range base {
		switch i % 5 {
		case 1:
			b = Block{CID: CIDv1(CodecRaw, MhSHA2_512, b.Data), Data: b.Data}
		case 2:
			b = Block{CID: CIDv1(CodecRaw, MhSHA3_256, b.Data), Data: b.Data}
		case 3:
			b = Block{CID: CIDv1(CodecRaw, MhFakeBlake, b.Data), Data: b.Data}
		case 4:
			if len(b.Data) <= 40 {
				b = Block{CID: CIDv1(CodecRaw, MhIdentity, b.Data), Data: b.Data}
			}
		}
		mix = append(mix, b)
	}
	fams = append(fams, Family{Name: name("v1-mixhash"), Note: "sha2-256/512, sha3-256, unverifiable blake-class and identity CIDs in one archive", Revs: []Rev{
		revBlocks("r0 mixed", sc.N, hdr, mix),
		revBlocks("r1 mixed shuffled", 0, hdr, Shuffled(5, mix)),
	}})

	// ---- CARv2 ----
	wrap := func(label string, newb int, v1 []byte, o V2Opts) Rev {
		return Rev{Label: label, Data: V2From(v1, o), NewBlocks: newb}
	}
	v1b := V1With(hdr, base)
	mh := V2Opts{IndexCodec: MultihashIndexSortedCodec}
	fams = append(fams, Family{Name: name("v2-wrap"), Note: "one CARv1 payload under different CARv2 wrappers / padding / indexes", Revs: []Rev{
		{Label: "r0 CARv1", Data: v1b, NewBlocks: sc.N},
		wrap("r1 v2 no index", 0, v1b, V2Opts{}),
		wrap("r2 v2 MultihashIndexSorted", 0, v1b, mh),
		wrap("r3 v2 IndexSorted", 0, v1b, V2Opts{IndexCodec: IndexSortedCodec}),
		wrap("r4 v2 pad 4096/512 + MhIdx", 0, v1b, V2Opts{PadBefore: 4096, PadAfter: 512, IndexCodec: MultihashIndexSortedCodec}),
		wrap("r5 v2 pad 64 nonzero, char bit", 0, v1b, V2Opts{PadBefore: 64, PadAfter: 64, PadByte: 0x5a, Char: [16]byte{0x80}, IndexCodec: MultihashIndexSortedCodec}),
	}})

	sh := Shuffled(9, base)
	stale := V2From(V1With(hdr, sh), mh)
	oldIdx := V2From(v1b, mh)
	copy(stale[len(stale)-(len(oldIdx)-int(51+len(v1b))):], oldIdx[51+len(v1b):]) // stale index of the old order
	fams = append(fams, Family{Name: name("v2-reorder"), Note: "reordered blocks with the index regenerated (and once left stale)", Revs: []Rev{
		wrap("r0 v2 MhIdx", sc.N, v1b, mh),
		wrap("r1 shuffled, index regenerated", 0, V1With(hdr, sh), mh),
		wrap("r2 reversed, index regenerated", 0, V1With(hdr, reversed(sh)), mh),
		{Label: "r3 shuffled, STALE index", Data: stale},
	}})

	fams = append(fams, Family{Name: name("v2-add"), Note: "added blocks, index regenerated each time", Revs: []Rev{
		wrap("r0 v2 MhIdx", sc.N, v1b, mh),
		wrap("r1 +1 appended", 1, V1With(hdr, v1), mh),
		wrap("r2 +4 inserted", 4, V1With(hdr, v2), mh),
	}})
	return fams
}
