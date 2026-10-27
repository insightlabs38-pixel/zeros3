// Package lab compares candidate physical representations of an object with
// what current ZeroS3 CDC/CAS would already reuse.
//
// The primitives are deliberately few: a Source (range-readable bytes), a Plan
// (components a representation would store, recipe cost, optional exact
// rebuild), and an Acct (family-wide accounting of new vs reused components).
// This is research code; none of it is a proposed production contract.
package lab

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

type ID [32]byte

// Source is a bounded, range-readable byte source.
type Source struct {
	Name string
	R    io.ReaderAt
	Size int64
}

func BytesSource(name string, b []byte) Source {
	return Source{Name: name, R: bytes.NewReader(b), Size: int64(len(b))}
}

// OpenSource opens a file; the caller closes the returned closer.
func OpenSource(path string) (Source, io.Closer, error) {
	f, err := os.Open(path)
	if err != nil {
		return Source{}, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return Source{}, nil, err
	}
	return Source{Name: path, R: f, Size: st.Size()}, f, nil
}

var ErrRange = errors.New("lab: range outside source")

// Span returns a copy of [off, off+n). It rejects negative values and
// ranges whose end overflows or exceeds the source.
func (s Source) Span(off, n int64) ([]byte, error) {
	if off < 0 || n < 0 || off > s.Size || n > s.Size-off {
		return nil, fmt.Errorf("%w: off=%d n=%d size=%d", ErrRange, off, n, s.Size)
	}
	b := make([]byte, n)
	if _, err := s.R.ReadAt(b, off); err != nil && !(err == io.EOF && int64(len(b)) == n) {
		return nil, err
	}
	return b, nil
}

// Reader streams [off, off+n) without loading it.
func (s Source) Reader(off, n int64) (io.Reader, error) {
	if off < 0 || n < 0 || off > s.Size || n > s.Size-off {
		return nil, fmt.Errorf("%w: off=%d n=%d size=%d", ErrRange, off, n, s.Size)
	}
	return io.NewSectionReader(s.R, off, n), nil
}

// Comp is one physical component: a content-addressed blob in a hypothetical
// store. Size is the stored byte count charged when the component is new.
// Data yields the payload (for packing estimates and reconstruction).
type Comp struct {
	ID   ID
	Size int64
	Data func() ([]byte, error)
	// Class optionally labels the component ("plain", "unit", ...) for cost
	// breakdowns; it never affects identity or accounting totals.
	Class string
	// Pad is, for a whole tar member body, the number of trailing bytes that are tar
	// block padding (zeros); a nested transform works on Data[:Size-Pad] (Z3-19).
	Pad int
	// Pre is, for a whole Git object, the length of the "blob N\0" header before the
	// content a nested transform represents; the header bytes are carried in the
	// composition record (Z3-19).
	Pre int
}

// Piece is one element of a representation's recipe: a reference to a
// component, or bytes stored inline in the recipe itself (small regions).
type Piece struct {
	Comp   *Comp
	Inline []byte
	Len    int64 // bytes of the original object this piece reproduces (concat plans)
	// Via marks a component that is reachable through another component of the
	// same plan (a parent node or manifest lists its digest), so the recipe
	// carries no reference to it. It still counts as stored/reused content.
	Via bool
	// Derived marks a component whose bytes are NOT stored: it is produced at
	// rebuild time by another representation of the same plan (a nested transform,
	// Z3-19). Its reference is charged like any piece (the digest and length are in
	// the recipe and verified), its bytes are not.
	Derived bool
}

// Plan is what a representation would store for one revision.
type Plan struct {
	Pieces     []Piece
	MetaBytes  int64 // recipe bytes beyond per-reference and inline costs
	IndexBytes int64 // index/checkpoint bytes
	// Concat means the pieces, in order, are exactly the object bytes, which
	// enables generic range reads.
	Concat bool
	// Rebuild reproduces the original object bytes from components fetched via
	// get. Nil means the representation makes no reversibility claim.
	Rebuild func(w io.Writer, get func(ID) ([]byte, error)) error
	// Oracle marks an estimate with no reconstruction path (an upper bound on
	// what a transform could achieve, not a representation).
	Oracle bool
	// NoPack skips pack-compression estimation (e.g. the whole-object control).
	NoPack bool
	// NoCost marks an experimental representation with no physical encoding
	// yet: it is parsed and replayed and its structure reported, but no cost
	// is invented (rows show N/A).
	NoCost bool
	Detail map[string]any
}

// InlineRecipeOverhead is the per-inline-region length prefix charged in the recipe.
const InlineRecipeOverhead = 4

// Accounts for one revision under one representation.
type Account struct {
	Components       int   `json:"components"`
	UniqueComponents int   `json:"unique_components"`
	NewComponents    int   `json:"new_components"`
	ReusedComponents int   `json:"reused_components"`
	InlineBytes      int64 `json:"inline_bytes"`
	Derived          int   `json:"derived_refs,omitempty"`
	DerivedBytes     int64 `json:"derived_bytes,omitempty"`
	NewBytes         int64 `json:"new_semantic_bytes"`
	ReuseBytes       int64 `json:"reuse_bytes"`
	MetaBytes        int64 `json:"metadata_bytes"`
	IndexBytes       int64 `json:"index_bytes"`
	Incremental      int64 `json:"incremental_bytes"`
	Cumulative       int64 `json:"cumulative_bytes"`
	PackedKnown      bool  `json:"-"`
	PackedNew        int64 `json:"-"` // packed estimate of NewBytes
	IncrementalPack  int64 `json:"-"` // PackedNew + recipe bytes
	CumulativePack   int64 `json:"-"`
	// Per-class breakdown (components with a Class): new bytes, packed new
	// bytes, references (all, new or reused) and new component counts.
	ClassNew       map[string]int64 `json:"class_new_bytes,omitempty"`
	ClassPackedNew map[string]int64 `json:"class_packed_new_bytes,omitempty"`
	ClassRefs      map[string]int   `json:"class_refs,omitempty"`
	ClassNewComps  map[string]int   `json:"class_new_comps,omitempty"`
	// Z3-08H: serialised recipe bytes of the piece list under each layout
	// (excluding PlanMetaBytes), see recipe.go.
	Layouts       map[string]int64 `json:"recipe_layout_bytes,omitempty"`
	PlanMetaBytes int64            `json:"plan_meta_bytes,omitempty"`
}

// Acct tracks the components a representation has stored across a family.
type Acct struct {
	RefBytes int64 // recipe cost of one component reference
	Pack     bool  // estimate production pack compression of new components
	store    map[ID]Comp
	cum      int64
	cumPack  int64
}

func NewAcct(refBytes int64, pack bool) *Acct {
	return &Acct{RefBytes: refBytes, Pack: pack, store: map[ID]Comp{}}
}

// Clone returns an independent copy of the store (components are shared, not copied).
func (a *Acct) Clone() *Acct {
	c := *a
	c.store = make(map[ID]Comp, len(a.store))
	for k, v := range a.store {
		c.store[k] = v
	}
	return &c
}

// Add records one revision's plan and returns its incremental cost. The first
// occurrence of a component is new; later references (in this or any earlier
// revision) are reuse.
func (a *Acct) Add(p *Plan) (Account, error) {
	var ac Account
	ac.PackedKnown = a.Pack && !p.NoPack
	seen := map[ID]bool{}
	via := 0
	var added []Comp
	for _, pc := range p.Pieces {
		if pc.Comp == nil {
			ac.InlineBytes += int64(len(pc.Inline))
			continue
		}
		c := *pc.Comp
		if c.Size < 0 {
			return ac, fmt.Errorf("lab: negative component size %d", c.Size)
		}
		ac.Components++
		if pc.Via {
			via++
		}
		if pc.Derived {
			ac.Derived++
			ac.DerivedBytes += c.Size
			continue
		}
		if c.Class != "" {
			if ac.ClassRefs == nil {
				ac.ClassRefs = map[string]int{}
			}
			ac.ClassRefs[c.Class]++
		}
		if old, ok := a.store[c.ID]; ok {
			if old.Size != c.Size {
				return ac, fmt.Errorf("lab: component %x registered with sizes %d and %d", c.ID[:6], old.Size, c.Size)
			}
			ac.ReusedComponents++
			ac.ReuseBytes += c.Size
			continue
		}
		if seen[c.ID] {
			ac.ReusedComponents++
			ac.ReuseBytes += c.Size
			continue
		}
		seen[c.ID] = true
		ac.NewComponents++
		ac.NewBytes += c.Size
		added = append(added, c)
		if c.Class != "" {
			if ac.ClassNew == nil {
				ac.ClassNew, ac.ClassNewComps = map[string]int64{}, map[string]int{}
			}
			ac.ClassNew[c.Class] += c.Size
			ac.ClassNewComps[c.Class]++
		}
	}
	// UniqueComponents counts distinct ids referenced, new or already stored.
	ids := map[ID]bool{}
	for _, pc := range p.Pieces {
		if pc.Comp != nil {
			ids[pc.Comp.ID] = true
		}
	}
	ac.UniqueComponents = len(ids)
	ac.MetaBytes = a.RefBytes*int64(ac.Components-via) + ac.InlineBytes + InlineRecipeOverhead*int64(inlineCount(p)) + p.MetaBytes
	ac.IndexBytes = p.IndexBytes
	ac.Layouts, ac.PlanMetaBytes = recipeBytes(p), p.MetaBytes
	ac.Incremental = ac.NewBytes + ac.MetaBytes + ac.IndexBytes
	if ac.PackedKnown {
		for _, c := range added {
			ps, err := packedSize(c)
			if err != nil {
				return ac, err
			}
			ac.PackedNew += ps
			if c.Class != "" {
				if ac.ClassPackedNew == nil {
					ac.ClassPackedNew = map[string]int64{}
				}
				ac.ClassPackedNew[c.Class] += ps
			}
		}
		ac.IncrementalPack = ac.PackedNew + ac.MetaBytes + ac.IndexBytes
	}
	for _, c := range added {
		a.store[c.ID] = c
	}
	a.cum += ac.Incremental
	a.cumPack += ac.IncrementalPack
	ac.Cumulative, ac.CumulativePack = a.cum, a.cumPack
	return ac, nil
}

func inlineCount(p *Plan) int {
	n := 0
	for _, pc := range p.Pieces {
		if pc.Comp == nil {
			n++
		}
	}
	return n
}

// Get returns a stored component's payload.
func (a *Acct) Get(id ID) ([]byte, error) {
	c, ok := a.store[id]
	if !ok {
		return nil, fmt.Errorf("lab: component %x not in store", id[:6])
	}
	if c.Data == nil {
		return nil, fmt.Errorf("lab: component %x has no payload", id[:6])
	}
	b, err := c.Data()
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Stored reports how many distinct components the store holds.
func (a *Acct) Stored() int { return len(a.store) }

// packedSize applies the production pack rule (zeros3.go packCompressor):
// DEFLATE at the default level, kept only if it saves at least 1/16.
func packedSize(c Comp) (int64, error) {
	if c.Data == nil {
		return c.Size, nil
	}
	b, err := c.Data()
	if err != nil {
		return 0, err
	}
	return packedLen(b), nil
}

// packedCache memoises the pack estimate by content digest: the same component is
// evaluated many times across representations and runs.
var packedCache sync.Map

func packedLen(b []byte) int64 {
	if len(b) == 0 {
		return 0
	}
	if len(b) >= 256 {
		id := sum(b)
		if v, ok := packedCache.Load(id); ok {
			return v.(int64)
		}
		n := packedLenRaw(b)
		packedCache.Store(id, n)
		return n
	}
	return packedLenRaw(b)
}

var flatePool = sync.Pool{New: func() any { w, _ := flate.NewWriter(io.Discard, flate.DefaultCompression); return w }}

func packedLenRaw(b []byte) int64 {
	var cw countWriter
	fw := flatePool.Get().(*flate.Writer)
	fw.Reset(&cw)
	fw.Write(b)
	fw.Close()
	flatePool.Put(fw)
	if cw.n <= int64(len(b)-max(1, len(b)/16)) {
		return cw.n
	}
	return int64(len(b))
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

func sum(b []byte) ID { return sha256.Sum256(b) }

// ReadRange serves [off, off+n) from a Concat plan, returning the bytes and
// the number of component bytes fetched (for read amplification).
func ReadRange(p *Plan, get func(ID) ([]byte, error), off, n int64) (out []byte, fetched int64, err error) {
	if !p.Concat {
		return nil, 0, errors.New("lab: plan does not support range reads")
	}
	if off < 0 || n < 0 {
		return nil, 0, ErrRange
	}
	var total int64
	for _, pc := range p.Pieces {
		total += pc.Len
	}
	if off > total || n > total-off {
		return nil, 0, fmt.Errorf("%w: off=%d n=%d size=%d", ErrRange, off, n, total)
	}
	if n == 0 {
		return nil, 0, nil
	}
	var pos int64
	for _, pc := range p.Pieces {
		end := pos + pc.Len
		if end > off && pos < off+n {
			var blob []byte
			if pc.Comp != nil {
				if blob, err = get(pc.Comp.ID); err != nil {
					return nil, fetched, err
				}
				fetched += int64(len(blob))
			} else {
				blob = pc.Inline
			}
			if int64(len(blob)) != pc.Len {
				return nil, fetched, fmt.Errorf("lab: piece length %d, payload %d", pc.Len, len(blob))
			}
			lo, hi := max(off, pos)-pos, min(off+n, end)-pos
			out = append(out, blob[lo:hi]...)
		}
		pos = end
		if pos >= off+n {
			break
		}
	}
	return out, fetched, nil
}

// ConcatRebuild is the Rebuild function of a Concat plan.
func ConcatRebuild(p *Plan) func(io.Writer, func(ID) ([]byte, error)) error {
	return func(w io.Writer, get func(ID) ([]byte, error)) error {
		for _, pc := range p.Pieces {
			blob := pc.Inline
			if pc.Comp != nil {
				b, err := get(pc.Comp.ID)
				if err != nil {
					return err
				}
				blob = b
			}
			if _, err := w.Write(blob); err != nil {
				return err
			}
		}
		return nil
	}
}
