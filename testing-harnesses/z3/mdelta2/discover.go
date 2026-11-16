package mdelta2

import (
	"encoding/binary"
	"hash/fnv"
	"math"
	"sort"

	"zeros3-z3/simdelta"
)

// PT is one tensor of the discovery pool, in its coding dtype (exact F32->F16/BF16 narrowing applied).
type PT struct {
	Model int
	Idx   int
	Name  string
	DT    DType
	Shape []int64
	Data  []byte
	Elems int
	SHA   SHA
	// sketches
	FP   []uint64  // 64 position-aligned 8-byte window hashes
	Num  []float64 // 64 sampled values
	Blk  []float64 // 16 blocks x (mean|x|, mean sign)
	Rows map[uint64]bool
	B1   []uint64 // bottom-k substring features
	B1S  []uint64 // positional sign LSH bands (F32 layout)
	B1C  []uint64 // coarse numeric features
}

// NewPTLite builds the sketches and drops the payload (discovery over large pools).
func NewPTLite(model, idx int, t *Tensor, data []byte) *PT {
	p := NewPT(model, idx, t, data)
	p.Data = nil
	return p
}

func NewPT(model, idx int, t *Tensor, data []byte) *PT {
	dt := t.DType
	if dt == DF32 {
		if nd, ok := Narrow(data, DF16); ok {
			dt, data = DF16, nd
		}
	}
	p := &PT{Model: model, Idx: idx, Name: t.Name, DT: dt, Shape: t.Shape, Data: data, Elems: int(t.Elems()), SHA: sum(data)}
	p.sketch()
	return p
}

func h64(b []byte) uint64 { f := fnv.New64a(); f.Write(b); return f.Sum64() }

func (p *PT) sketch() {
	w := p.DT.Width()
	n := p.Elems
	if n < 64 || !p.DT.IsFloat() {
		return
	}
	p.FP = make([]uint64, 64)
	p.Num = make([]float64, 64)
	base := Base{DT: p.DT, Data: p.Data}
	for i := 0; i < 64; i++ {
		pos := (i * (n - 1) / 63)
		end := pos*w + 8
		if end > len(p.Data) {
			end = len(p.Data)
		}
		p.FP[i] = h64(p.Data[pos*w : end])
		p.Num[i] = base.At(pos)
	}
	blk := n / 16
	p.Blk = make([]float64, 32)
	for b := 0; b < 16; b++ {
		var ma, ms float64
		for i := b * blk; i < (b+1)*blk; i++ {
			x := base.At(i)
			if !finite(x) {
				continue
			}
			ma += math.Abs(x)
			if x > 0 {
				ms++
			}
		}
		p.Blk[2*b], p.Blk[2*b+1] = ma/float64(blk), ms/float64(blk)
	}
	rows, cols := 1, n
	if len(p.Shape) >= 2 {
		cols = int(p.Shape[len(p.Shape)-1])
		rows = n / cols
	}
	if rows >= 8 && cols >= 16 {
		p.Rows = map[uint64]bool{}
		for r := 0; r < rows; r++ {
			var sig uint64
			var l1 float64
			for c := 0; c < cols; c++ {
				l1 += math.Abs(base.At(r*cols + c))
			}
			for k := 0; k < 32; k++ {
				if base.At(r*cols+k*(cols-1)/31) > 0 {
					sig |= 1 << uint(k)
				}
			}
			lg := int64(4 * math.Log2(l1/float64(cols)+1e-30))
			p.Rows[sig*0x9e3779b97f4a7c15^uint64(lg)*0xbf58476d1ce4e5b9] = true
		}
	}
	raw := p.Data
	if len(raw) > 1<<20 {
		raw = raw[:1<<20]
	}
	p.B1 = simdelta.Features(raw, 16)
	p.B1S = simdelta.SignFeatures(p.Data)
	p.B1C = simdelta.CoarseFeatures(raw)
}

// Method names for the discovery comparison.
var DiscMethods = []string{"sha", "name+shape", "sampled-bytes", "num-sketch", "block-stats", "row-fp", "b1-substring", "b1-sign-lsh", "b1-coarse", "combined"}

func overlap(a, b []uint64) int {
	m := map[uint64]bool{}
	for _, x := range a {
		m[x] = true
	}
	n := 0
	for _, x := range b {
		if m[x] {
			n++
			delete(m, x)
		}
	}
	return n
}

// Score returns the similarity of target t to pool tensor p under the method (higher = more similar).
func Score(method string, t, p *PT) float64 {
	switch method {
	case "sha":
		if t.SHA == p.SHA {
			return 1
		}
		return 0
	case "name+shape":
		if t.Name == p.Name && t.DT == p.DT && len(t.Shape) == len(p.Shape) && shapeEq(t.Shape, p.Shape) {
			return 1
		}
		return 0
	case "sampled-bytes":
		if t.FP == nil || p.FP == nil {
			return 0
		}
		n := 0
		for i := range t.FP {
			if t.FP[i] == p.FP[i] {
				n++
			}
		}
		return float64(n)
	case "num-sketch":
		if t.Num == nil || p.Num == nil {
			return 0
		}
		var d, s float64
		for i := range t.Num {
			d += math.Abs(t.Num[i] - p.Num[i])
			s += math.Abs(t.Num[i]) + math.Abs(p.Num[i])
		}
		return 1 - d/(s+1e-30)
	case "block-stats":
		if t.Blk == nil || p.Blk == nil {
			return 0
		}
		var d, s float64
		for i := range t.Blk {
			d += math.Abs(t.Blk[i] - p.Blk[i])
			s += math.Abs(t.Blk[i]) + math.Abs(p.Blk[i])
		}
		return 1 - d/(s+1e-30)
	case "row-fp":
		if t.Rows == nil || p.Rows == nil {
			return 0
		}
		n := 0
		for k := range t.Rows {
			if p.Rows[k] {
				n++
			}
		}
		return float64(n) / float64(len(t.Rows))
	case "b1-substring":
		return float64(overlap(t.B1, p.B1))
	case "b1-sign-lsh":
		return float64(overlap(t.B1S, p.B1S))
	case "b1-coarse":
		return float64(overlap(t.B1C, p.B1C))
	case "combined":
		return Score("num-sketch", t, p) + Score("block-stats", t, p) + 0.5*Score("row-fp", t, p) + 0.02*Score("sampled-bytes", t, p)
	}
	return 0
}

func shapeEq(a, b []int64) bool {
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

// Rank orders pool indexes by descending score (stable on index), dropping zero scores.
func Rank(method string, t *PT, pool []*PT) []int {
	idx := make([]int, 0, len(pool))
	sc := make([]float64, len(pool))
	for i, p := range pool {
		sc[i] = Score(method, t, p)
		if sc[i] > 0 {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool { return sc[idx[a]] > sc[idx[b]] })
	return idx
}

// PairCost is the estimated coded bytes of t predicted from p by identity (key residual, NRC proxy);
// +Inf when p cannot serve as a base.
func PairCost(t, p *PT) float64 {
	if p.Elems != t.Elems || !t.DT.IsFloat() || !p.DT.IsFloat() {
		return math.Inf(1)
	}
	pr := &Params{Kind: KID, Res: RKey, NB: 1, Prec: 4}
	pred, err := Predict(pr, t.DT, t.Shape, []Base{{DT: p.DT, Data: p.Data}})
	if err != nil {
		return math.Inf(1)
	}
	return nrcProxy(t.DT, MakeResidual(RKey, t.DT.Width(), t.Data, pred), pred)
}

// DirectCost is the no-base estimate (raw words, NRC proxy with a zero prediction).
func DirectCost(t *PT) float64 {
	if !t.DT.IsFloat() {
		return float64(len(t.Data))
	}
	best := float64(PlaneDeflate(t.Data, t.DT.Width()))
	if f, ok := EncodeResidual(CFPE, t.DT, RXor, t.Data, make([]byte, len(t.Data))); ok && float64(len(f)) < best {
		best = float64(len(f))
	}
	return best
}

var _ = binary.LittleEndian
