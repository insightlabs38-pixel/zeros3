package modelz3

import (
	"crypto/sha256"
	"io"
	"runtime"
	"sync"

	"zeros3-z3/lab"
	"zeros3-z3/repz3"
)

type SHA = [32]byte

// Src is a file the components of a CompList live in.
type Src struct {
	R    io.ReaderAt
	Size int64
	Name string
}

// Comp is one content-addressed component: a byte range of a file (Off/Size) with its digest.
type Comp struct {
	ID   SHA
	Size int64
	Src  *Src
	Off  int64
	Kind string // "meta" | "gap" | "tail" | "tensor" | "chunk" | "block"
}

// CompList is the component sequence of one representation of one file (recipe order).
type CompList []Comp

// Bytes is the logical size covered.
func (l CompList) Bytes() (n int64) {
	for _, c := range l {
		n += c.Size
	}
	return
}

// Packer computes and caches the physical size of components under the production pack rule
// (DEFLATE default, kept only if it saves 1/16: lab.PackedLen), reading bytes from their Src.
type Packer struct {
	mu    sync.Mutex
	cache map[SHA]int64
}

func NewPacker() *Packer { return &Packer{cache: map[SHA]int64{}} }

func workers() int { return max(1, min(4, runtime.NumCPU())) }

// Fill packs every component of cs (distinct ids) not yet cached, in parallel, bounded by memory:
// at most workers() components are resident at once.
func (p *Packer) Fill(cs []Comp) error {
	todo := make([]Comp, 0, len(cs))
	seen := map[SHA]bool{}
	p.mu.Lock()
	for _, c := range cs {
		if _, ok := p.cache[c.ID]; !ok && !seen[c.ID] {
			seen[c.ID] = true
			todo = append(todo, c)
		}
	}
	p.mu.Unlock()
	var wg sync.WaitGroup
	ch := make(chan Comp)
	errc := make(chan error, workers())
	for i := 0; i < workers(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var buf []byte
			for c := range ch {
				if int64(cap(buf)) < c.Size {
					buf = make([]byte, c.Size)
				}
				b := buf[:c.Size]
				if err := readFull(c.Src.R, b, c.Off); err != nil {
					errc <- err
					return
				}
				n := lab.PackedLen(b)
				p.mu.Lock()
				p.cache[c.ID] = n
				p.mu.Unlock()
			}
		}()
	}
	for _, c := range todo {
		select {
		case ch <- c:
		case err := <-errc:
			close(ch)
			wg.Wait()
			return err
		}
	}
	close(ch)
	wg.Wait()
	select {
	case err := <-errc:
		return err
	default:
	}
	return nil
}

// PutPacked records the packed size of bytes that are not a file range (a residual).
func (p *Packer) PutPacked(id SHA, b []byte) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n, ok := p.cache[id]; ok {
		return n
	}
	n := lab.PackedLen(b)
	p.cache[id] = n
	return n
}

func (p *Packer) get(id SHA) int64 { p.mu.Lock(); defer p.mu.Unlock(); return p.cache[id] }

// Cost is the physical increment of storing a representation given what is already stored.
type Cost struct {
	Raw, Packed int64 // new component bytes (payload), raw and under the pack rule
	Recipe      int64 // representation object bytes (added to both)
	Objects     int   // new components + the representation object
	Reused      int64 // bytes of the target covered by components already stored
}

// Total returns the cost with or without the pack rule applied to components.
func (c Cost) Total(packed bool) int64 {
	if packed {
		return c.Packed + c.Recipe
	}
	return c.Raw + c.Recipe
}

// RecipeBytes is the exact serialized size of the KindConcat representation of the list (the
// repz3 contract object a CDC or structural split stores), sizes only.
func RecipeBytes(l CompList) int64 {
	r := &repz3.Rep{Kind: repz3.KindConcat, Size: l.Bytes()}
	for _, c := range l {
		r.Members = append(r.Members, repz3.Member{Role: repz3.Stored, SHA: c.ID, Size: c.Size})
	}
	b, err := r.Marshal()
	if err != nil {
		panic(err)
	}
	return int64(len(b))
}

// Seen is the set of component ids a hypothetical store already holds.
type Seen map[SHA]bool

// Add marks every component of l as stored.
func (s Seen) Add(l CompList) {
	for _, c := range l {
		s[c.ID] = true
	}
}

// CostOf prices storing l (and its recipe) when seen is already stored.
func CostOf(l CompList, seen Seen, p *Packer) (Cost, error) {
	var news []Comp
	local := map[SHA]bool{}
	var c Cost
	for _, x := range l {
		if seen[x.ID] || local[x.ID] {
			c.Reused += x.Size
			continue
		}
		local[x.ID] = true
		news = append(news, x)
		c.Raw += x.Size
	}
	if err := p.Fill(news); err != nil {
		return c, err
	}
	for _, x := range news {
		c.Packed += p.get(x.ID)
	}
	c.Recipe = RecipeBytes(l)
	c.Objects = len(news) + 1
	return c, nil
}

// digest hashes the range [off,off+n) of src.
func digest(src *Src, off, n int64, buf []byte) (SHA, []byte, error) {
	if int64(cap(buf)) < n {
		buf = make([]byte, n)
	}
	b := buf[:n]
	if err := readFull(src.R, b, off); err != nil {
		return SHA{}, buf, err
	}
	return sha256.Sum256(b), buf, nil
}

// readFull is ReadAt that treats a short read as an error (io.EOF is fine when the buffer was filled).
func readFull(r io.ReaderAt, b []byte, off int64) error {
	n, err := r.ReadAt(b, off)
	if n == len(b) {
		return nil
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return err
}

func sha256sum(b []byte) SHA { return sha256.Sum256(b) }

func packedLen(b []byte) int64 { return lab.PackedLen(b) }

// PackedLen is the production pack rule's stored size of b.
func PackedLen(b []byte) int64 { return lab.PackedLen(b) }
