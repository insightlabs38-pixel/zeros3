package semcore

import "errors"

// IO counts the physical work of range reads: bytes read from blobs, distinct 4 KiB pages
// touched and bytes materialized through full-payload fallbacks.
type IO struct {
	Bytes, Materialized int64
	pages               map[[2]uint64]struct{}
	Reads               int
	// Verified-block accounting (Store.BlockSize > 0): blocks fetched and hashed in this
	// request, and the one-time whole-blob verification charged to the first touch.
	BlocksRead      int
	FirstTouches    int
	FirstTouchBytes int64
	blocks          map[blockKey][]byte
}

func (io *IO) page(blob ID, off, n int64) {
	if io.pages == nil {
		io.pages = map[[2]uint64]struct{}{}
	}
	for p := off / 4096; p <= (off+n-1)/4096 && n > 0; p++ {
		io.pages[[2]uint64{uint64(blob[0])<<8 | uint64(blob[1]) | uint64(blob[2])<<16 | uint64(blob[3])<<24, uint64(p)}] = struct{}{}
	}
}

// Pages is the number of distinct 4 KiB blob pages touched.
func (io *IO) Pages() int { return len(io.pages) }

// ReadRange returns [off, off+n) of the original file described by the recipe, touching as
// little stored data as the derivation structure allows. Elementwise and block-local ops
// (convert, byte swap, Q8_0/Q4_0, slice), row gathers and permutations are evaluated on the
// needed cells only; everything else materializes the whole component.
func (s *Store) ReadRange(r *Recipe, off, n int64, io *IO) ([]byte, error) {
	if off < 0 || n < 0 || off+n > r.Size {
		return nil, errors.New("semcore: range out of file")
	}
	if io == nil {
		io = &IO{}
	}
	out := make([]byte, 0, n)
	pos := int64(0)
	for i := range r.Steps {
		st := &r.Steps[i]
		var ln int64
		switch st.Kind {
		case StepLit:
			ln = int64(len(st.Lit))
		case StepRef:
			rec := s.Rec(st.CID)
			if rec == nil {
				return nil, ErrMissing
			}
			ln = rec.PayloadLen
		default:
			ln = int64(st.Len)
		}
		lo, hi := max(off, pos), min(off+n, pos+ln)
		if lo < hi {
			a, b := lo-pos, hi-pos
			switch st.Kind {
			case StepLit:
				out = append(out, st.Lit[a:b]...)
			case StepRef:
				p, err := s.readComp(st.CID, a, b, io)
				if err != nil {
					return nil, err
				}
				out = append(out, p...)
			default:
				p, err := s.readComp(st.CID, int64(st.Off)+a, int64(st.Off)+b, io)
				if err != nil {
					return nil, err
				}
				out = append(out, p...)
			}
		}
		pos += ln
		if pos >= off+n {
			break
		}
	}
	return out, nil
}

func (s *Store) readComp(c ID, a, b int64, io *IO) ([]byte, error) {
	r := s.Rec(c)
	if r == nil {
		return nil, ErrMissing
	}
	if a < 0 || b > r.PayloadLen || a > b {
		return nil, ErrCorrupt
	}
	if !r.Derived() && s.BlockSize > 0 {
		return s.readVerified(r.Payload, r.PayloadLen, a, b, io)
	}
	if !r.Derived() {
		f, err := s.B.OpenBlob(r.Payload)
		if err != nil {
			return nil, ErrMissing
		}
		defer f.Close()
		buf := make([]byte, b-a)
		if _, err := f.ReadAt(buf, a); err != nil && int64(len(buf)) > 0 {
			return nil, err
		}
		io.Bytes += b - a
		io.Reads++
		io.page(r.Payload, a, b-a)
		return buf, nil
	}
	if len(r.Parents) == 1 && localPipeline(r.Ops) {
		out, err := s.readOps(r.Parents[0], r.Ops, a, b, io)
		if err != nil {
			return nil, err
		}
		for _, p := range r.Patches {
			lo, hi := max(int64(p.Off), a), min(int64(p.Off)+int64(len(p.Data)), b)
			if lo < hi {
				copy(out[lo-a:hi-a], p.Data[lo-int64(p.Off):hi-int64(p.Off)])
			}
		}
		return out, nil
	}
	full, err := s.Payload(c)
	if err != nil {
		return nil, err
	}
	io.Materialized += int64(len(full))
	io.Bytes += int64(len(full))
	return full[a:b], nil
}

func localPipeline(ops []Op) bool {
	for i := range ops {
		switch ops[i].Kind {
		case OpConvert, OpBswap, OpQ8_0, OpQ4_0, OpSlice, OpGather, OpPerm:
		default:
			return false
		}
	}
	return true
}

// readOps returns bytes [a,b) of ops applied to the parent payload.
func (s *Store) readOps(parent ID, ops []Op, a, b int64, io *IO) ([]byte, error) {
	if len(ops) == 0 {
		return s.readComp(parent, a, b, io)
	}
	o := &ops[len(ops)-1]
	rest := ops[:len(ops)-1]
	align := func(inBlock, outBlock int64) ([]byte, int64, error) {
		oa, ob := a/outBlock*outBlock, (b+outBlock-1)/outBlock*outBlock
		blocks := ob/outBlock - oa/outBlock
		in, err := s.readOps(parent, rest, oa/outBlock*inBlock, oa/outBlock*inBlock+blocks*inBlock, io)
		return in, oa, err
	}
	var full []byte
	var oa int64
	var err error
	switch o.Kind {
	case OpConvert:
		fw, tw := int64(ElemBytes(int(o.From))), int64(ElemBytes(int(o.To)))
		var in []byte
		in, oa, err = align(fw, tw)
		if err == nil {
			full, err = o.Apply([][]byte{in})
		}
	case OpBswap:
		w := int64(o.Elem)
		var in []byte
		in, oa, err = align(w, w)
		if err == nil {
			full, err = o.Apply([][]byte{in})
		}
	case OpQ8_0, OpQ4_0:
		ob := int64(34)
		if o.Kind == OpQ4_0 {
			ob = 18
		}
		var in []byte
		in, oa, err = align(128, ob)
		if err == nil {
			full, err = o.Apply([][]byte{in})
		}
	case OpSlice:
		in, e := s.readOps(parent, rest, int64(o.Off)+a, int64(o.Off)+b, io)
		return in, e
	case OpGather:
		rb := int64(o.Elem)
		for r := a / rb; r*rb < b; r++ {
			row, e := s.readOps(parent, rest, int64(o.Idx[r])*rb, int64(o.Idx[r])*rb+rb, io)
			if e != nil {
				return nil, e
			}
			lo, hi := max(a, r*rb), min(b, (r+1)*rb)
			full = append(full, row[lo-r*rb:hi-r*rb]...)
		}
		return full, nil
	case OpPerm:
		return s.readPerm(parent, rest, o, a, b, io)
	}
	if err != nil {
		return nil, err
	}
	return full[a-oa : b-oa], nil
}

// readPerm evaluates a permutation for the output cells in [a,b) by inverse index mapping.
func (s *Store) readPerm(parent ID, rest []Op, o *Op, a, b int64, io *IO) ([]byte, error) {
	e := int64(o.Elem)
	r := len(o.Dims)
	odims := make([]int64, r)
	sstr := make([]int64, r)
	st := int64(1)
	for i := r - 1; i >= 0; i-- {
		sstr[i] = st
		st *= int64(o.Dims[i])
	}
	ostr := make([]int64, r)
	for i, ax := range o.Axes {
		odims[i] = int64(o.Dims[ax])
		ostr[i] = sstr[ax]
	}
	// count discontiguous source runs first: many runs means the whole tensor is read once
	runs, prev := 0, int64(-2)
	for cell := a / e; cell*e < b && runs <= maxPermRuns; cell++ {
		src, rem := int64(0), cell
		for i := r - 1; i >= 0; i-- {
			src += (rem % odims[i]) * ostr[i]
			rem /= odims[i]
		}
		if src != prev+1 {
			runs++
		}
		prev = src
	}
	if runs > maxPermRuns && len(rest) == 0 && s.BlockSize > 0 {
		// parent is a raw component with verified blocks: gather the needed cells through the
		// request-local block cache instead of materializing the tensor. Cost is the number of
		// distinct blocks touched; if that exceeds half the tensor the whole read is cheaper.
		total := e
		for _, d := range o.Dims {
			total *= int64(d)
		}
		seen := map[int64]struct{}{}
		for cell := a / e; cell*e < b; cell++ {
			src, rem := int64(0), cell
			for i := r - 1; i >= 0; i-- {
				src += (rem % odims[i]) * ostr[i]
				rem /= odims[i]
			}
			seen[src*e/int64(s.BlockSize)] = struct{}{}
		}
		if int64(len(seen))*int64(s.BlockSize) <= total/2 {
			out := make([]byte, 0, b-a)
			base := a / e * e
			for cell := a / e; cell*e < b; cell++ {
				src, rem := int64(0), cell
				for i := r - 1; i >= 0; i-- {
					src += (rem % odims[i]) * ostr[i]
					rem /= odims[i]
				}
				p, err := s.readOps(parent, rest, src*e, src*e+e, io)
				if err != nil {
					return nil, err
				}
				out = append(out, p...)
			}
			return out[a-base : b-base], nil
		}
	}
	if runs > maxPermRuns {
		total := e
		for _, d := range o.Dims {
			total *= int64(d)
		}
		whole, err := s.readOps(parent, rest, 0, total, io)
		if err != nil {
			return nil, err
		}
		io.Materialized += total
		full, err := o.Apply([][]byte{whole})
		if err != nil {
			return nil, err
		}
		return full[a:b], nil
	}
	var out []byte
	var runStart, runLen int64 = -1, 0
	flush := func() error {
		if runLen == 0 {
			return nil
		}
		p, err := s.readOps(parent, rest, runStart*e, (runStart+runLen)*e, io)
		out = append(out, p...)
		runLen = 0
		return err
	}
	for cell := a / e; cell*e < b; cell++ {
		// decompose output cell index
		src, rem := int64(0), cell
		for i := r - 1; i >= 0; i-- {
			src += (rem % odims[i]) * ostr[i]
			rem /= odims[i]
		}
		if runLen > 0 && src == runStart+runLen {
			runLen++
		} else {
			if err := flush(); err != nil {
				return nil, err
			}
			runStart, runLen = src, 1
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	base := a / e * e
	return out[a-base : b-base], nil
}

// maxPermRuns bounds per-run reads for a permuted range before falling back to the whole tensor.
const maxPermRuns = 64
