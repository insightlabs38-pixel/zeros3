package repz3

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"math"
)

// Decoder is a format's reconstruction logic. It sees only the parsed
// representation, the bounded source and the writer; it must not keep state
// between calls. Decode writes the exact output progressively; Expand reports the
// VIA components (those named only inside stored components) without writing.
type Decoder interface {
	Decode(c *Call) error
	Expand(c *Call, emit func(SHA)) error
}

// Table maps a kind to its decoder: a finite switch in code, never a registry
// filled at start-up. KindConcat is built into this package and never consults it.
type Table func(Kind) (Decoder, bool)

// Env is everything a reconstruction or walk needs besides the representation.
type Env struct {
	Src    Source
	Table  Table
	Limits Limits

	// ExpandOnly is a laboratory knob for the reachability study: owning sections
	// are ignored and every kind's Expand names the VIA components.
	ExpandOnly bool

	noVerify bool // tests only: skip the representation digest check
}

// Call is one decoder invocation.
type Call struct {
	Rep *Rep
	ID  SHA       // the representation object's digest
	W   io.Writer // nil during Expand

	env      *Env
	depth    int
	path     []SHA // ancestors including ID
	declared map[SHA]int64
	scratch  []byte
}

// LoadRep fetches a representation object by digest, verifies the digest and
// parses it.
func (e *Env) LoadRep(id SHA) (*Rep, error) { return e.load(id, 1, nil) }

func (e *Env) load(id SHA, depth int, path []SHA) (*Rep, error) {
	lim := e.Limits.norm()
	if depth > lim.MaxDepth {
		return nil, fmt.Errorf("%w: %x at depth %d (maximum %d)", ErrDepth, id[:6], depth, lim.MaxDepth)
	}
	for _, p := range path {
		if p == id {
			return nil, fmt.Errorf("%w: %x", ErrCycle, id[:6])
		}
	}
	size, err := e.Src.Size(id)
	if err != nil {
		return nil, err
	}
	if size < 0 || size > lim.MaxRepBytes {
		return nil, fmt.Errorf("%w: representation %x is %d bytes", ErrLimit, id[:6], size)
	}
	b := make([]byte, size)
	if err := readFull(e.Src, id, b, 0); err != nil {
		return nil, err
	}
	if !e.noVerify && sha256.Sum256(b) != id {
		return nil, fmt.Errorf("%w: representation %x does not hash to its address", ErrMismatch, id[:6])
	}
	return Unmarshal(b, lim)
}

func (e *Env) decoder(k Kind) (Decoder, error) {
	if e.Table != nil {
		if d, ok := e.Table(k); ok {
			return d, nil
		}
	}
	return nil, fmt.Errorf("%w: no decoder for kind %d", ErrUnsupported, k)
}

func (e *Env) call(id SHA, rep *Rep, depth int, path []SHA, w io.Writer) *Call {
	p := make([]SHA, len(path)+1)
	copy(p, path)
	p[len(path)] = id
	return &Call{Rep: rep, ID: id, W: w, env: e, depth: depth, path: p}
}

// Reconstruct writes the exact output of representation id to w and returns its
// length and SHA-256 for the caller to compare with the object's identity. Bytes
// are written before the final checks complete: on any error the caller discards
// what was written. There is no fallback; a missing or corrupt object is an error.
func (e *Env) Reconstruct(id SHA, w io.Writer) (int64, SHA, error) {
	rep, err := e.load(id, 1, nil)
	if err != nil {
		return 0, SHA{}, err
	}
	h := sha256.New()
	if err := e.run(id, rep, 1, nil, io.MultiWriter(w, h)); err != nil {
		return 0, SHA{}, err
	}
	var sum SHA
	h.Sum(sum[:0])
	return rep.Size, sum, nil
}

// bound is the writer every decoder gets: it refuses output beyond the declared
// size, so a hostile recipe cannot produce unbounded bytes.
type bound struct {
	w   io.Writer
	n   int64
	max int64
}

func (b *bound) Write(p []byte) (int, error) {
	if int64(len(p)) > b.max-b.n {
		return 0, fmt.Errorf("%w: output exceeds the declared %d bytes", ErrMismatch, b.max)
	}
	n, err := b.w.Write(p)
	if n < 0 || n > len(p) {
		return 0, fmt.Errorf("repz3: writer returned %d for %d bytes", n, len(p))
	}
	b.n += int64(n)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

func (e *Env) run(id SHA, rep *Rep, depth int, path []SHA, w io.Writer) error {
	bw := &bound{w: w, max: rep.Size}
	c := e.call(id, rep, depth, path, bw)
	var err error
	if rep.Kind == KindConcat {
		err = c.decodeConcat()
	} else {
		var d Decoder
		if d, err = e.decoder(rep.Kind); err == nil {
			err = d.Decode(c)
		}
	}
	if err != nil {
		return err
	}
	if bw.n != rep.Size {
		return fmt.Errorf("%w: wrote %d bytes, representation declares %d", ErrMismatch, bw.n, rep.Size)
	}
	return nil
}

func (c *Call) decodeConcat() error {
	for i := range c.Rep.Members {
		if err := c.writeMember(&c.Rep.Members[i], c.W); err != nil {
			return fmt.Errorf("member %d: %w", i, err)
		}
	}
	return nil
}

// WriteMember streams member i to w in bounded pieces (a stored component, or a
// derived member rebuilt progressively from its child), checking length and
// SHA-256 as decodeConcat does.
func (c *Call) WriteMember(i int, w io.Writer) error {
	if i < 0 || i >= len(c.Rep.Members) {
		return corrupt("member %d of %d", i, len(c.Rep.Members))
	}
	return c.writeMember(&c.Rep.Members[i], w)
}

func (c *Call) writeMember(m *Member, w io.Writer) error {
	if m.Role == Derived {
		return c.streamDerived(m, w)
	}
	return c.streamStored(m, w)
}

// streamStored copies one component to w in bounded pieces, checking its length
// before the first read and its SHA-256 after the last.
func (c *Call) streamStored(m *Member, w io.Writer) error {
	size, err := c.env.Src.Size(m.SHA)
	if err != nil {
		return err
	}
	if size != m.Size {
		return fmt.Errorf("%w: %x is %d bytes, recipe says %d", ErrMismatch, m.SHA[:6], size, m.Size)
	}
	if c.scratch == nil {
		c.scratch = make([]byte, 64<<10)
	}
	h := sha256.New()
	for off := int64(0); off < size; {
		n := int(min(int64(len(c.scratch)), size-off))
		if err := readFull(c.env.Src, m.SHA, c.scratch[:n], off); err != nil {
			return err
		}
		h.Write(c.scratch[:n])
		if _, err := w.Write(c.scratch[:n]); err != nil {
			return err
		}
		off += int64(n)
	}
	var sum SHA
	h.Sum(sum[:0])
	if sum != m.SHA {
		return fmt.Errorf("%w: %x bytes do not hash to their address", ErrMismatch, m.SHA[:6])
	}
	return nil
}

var zeroBlock [4096]byte

// streamDerived produces a derived member: the child representation is loaded and
// verified by digest, reconstructed progressively between the frame's prefix and
// zero suffix, and the whole is checked against the member's length and SHA-256.
func (c *Call) streamDerived(m *Member, w io.Writer) error {
	child, err := c.env.load(m.Child, c.depth+1, c.path)
	if err != nil {
		return fmt.Errorf("child %x: %w", m.Child[:6], err)
	}
	f := m.Frame
	if int64(len(f.Prefix))+child.Size+int64(f.Zeros) != m.Size {
		return fmt.Errorf("%w: child %x is %d bytes, framed length %d (prefix %d, zeros %d)", ErrMismatch, m.Child[:6], child.Size, m.Size, len(f.Prefix), f.Zeros)
	}
	h := sha256.New()
	cw := &bound{w: io.MultiWriter(w, h), max: m.Size}
	if _, err := cw.Write(f.Prefix); err != nil {
		return err
	}
	if err := c.env.run(m.Child, child, c.depth+1, c.path, cw); err != nil {
		return fmt.Errorf("child %x: %w", m.Child[:6], err)
	}
	for z := f.Zeros; z > 0; {
		n := min(z, len(zeroBlock))
		if _, err := cw.Write(zeroBlock[:n]); err != nil {
			return err
		}
		z -= n
	}
	if cw.n != m.Size {
		return fmt.Errorf("%w: derived member wrote %d of %d bytes", ErrMismatch, cw.n, m.Size)
	}
	var sum SHA
	h.Sum(sum[:0])
	if sum != m.SHA {
		return fmt.Errorf("%w: child %x output does not hash to the member digest", ErrMismatch, m.Child[:6])
	}
	return nil
}

// Get fetches one whole component by digest for decoders that need all its bytes
// (a node, a table chunk, a page). The length is taken from the store and checked
// against the recipe when the representation lists the component, the cap is
// Limits.MaxWhole, and the bytes are hashed.
func (c *Call) Get(id SHA) ([]byte, error) {
	if c.declared == nil {
		c.declared = make(map[SHA]int64, len(c.Rep.Members))
		for i := range c.Rep.Members {
			if m := &c.Rep.Members[i]; m.Role == Stored {
				c.declared[m.SHA] = m.Size
			}
		}
	}
	decl := int64(-1)
	if s, ok := c.declared[id]; ok {
		decl = s
	}
	return ReadObject(c.env.Src, id, decl, c.env.Limits.norm().MaxWhole)
}

// GetExact is Get for a component the representation does not list as a member
// (a VIA component named by a table): the length must equal declared, which the
// decoder took from its own stored table, before any byte is read.
func (c *Call) GetExact(id SHA, declared int64) ([]byte, error) {
	if declared < 0 {
		return nil, corrupt("negative declared length")
	}
	return ReadObject(c.env.Src, id, declared, c.env.Limits.norm().MaxWhole)
}

// GetAny is Get for a component whose length only the store knows (a point read
// that followed a native record to it). The bytes are hashed against the id.
func (c *Call) GetAny(id SHA) ([]byte, error) {
	return ReadObject(c.env.Src, id, -1, c.env.Limits.norm().MaxWhole)
}

// Source is the bounded source this call reads through.
func (c *Call) Source() Source { return c.env.Src }

// Member returns the exact bytes of member i: a stored component, or a derived
// member rebuilt from its child (bounded by Limits.MaxWhole).
func (c *Call) Member(i int) ([]byte, error) {
	if i < 0 || i >= len(c.Rep.Members) {
		return nil, corrupt("member %d of %d", i, len(c.Rep.Members))
	}
	m := &c.Rep.Members[i]
	if m.Role == Stored {
		return c.Get(m.SHA)
	}
	if m.Size > c.env.Limits.norm().MaxWhole {
		return nil, fmt.Errorf("%w: derived member %d is %d bytes", ErrLimit, i, m.Size)
	}
	var buf bytes.Buffer
	if err := c.streamDerived(m, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Resolve maps a native identity to the component holding it by searching this
// representation's own record sections (O(log n) bounded reads each). The
// representation therefore carries everything it needs: no global state is
// consulted. The same key in several sections is fine when they agree; sections
// that disagree make the identity ambiguous and the answer is ErrAmbiguous,
// never a silent pick. found is false when no section knows it.
func (c *Call) Resolve(native []byte) (id SHA, found bool, err error) {
	for _, x := range c.Rep.Indexes {
		if len(native) != x.KeyWidth {
			continue
		}
		v, ok, err := LookupIndex(c.env.Src, x, native)
		if err != nil {
			return SHA{}, false, err
		}
		if !ok {
			continue
		}
		if found && v != id {
			return SHA{}, false, fmt.Errorf("%w: %x", ErrAmbiguous, native[:min(len(native), 6)])
		}
		id, found = v, true
	}
	return id, found, nil
}

// LookupIndex binary-searches one native index component with bounded reads.
func LookupIndex(src Source, x Index, key []byte) (id SHA, found bool, err error) {
	if len(key) != x.KeyWidth {
		return id, false, nil
	}
	size, err := src.Size(x.Ref)
	if err != nil {
		return id, false, err
	}
	if x.Count < 0 || x.Count > math.MaxInt64/int64(x.KeyWidth+32) || size != x.Bytes() {
		return id, false, fmt.Errorf("%w: index component is %d bytes, recipe says %d records of %d", ErrMismatch, size, x.Count, x.KeyWidth+32)
	}
	w := x.KeyWidth + 32
	rec := make([]byte, w)
	lo, hi := int64(0), x.Count
	for lo < hi {
		mid := lo + (hi-lo)/2
		if err := readFull(src, x.Ref, rec, mid*int64(w)); err != nil {
			return id, false, err
		}
		switch c := bytes.Compare(rec[:x.KeyWidth], key); {
		case c == 0:
			copy(id[:], rec[x.KeyWidth:])
			return id, true, nil
		case c < 0:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return id, false, nil
}

// Open loads a representation for partial reads (no output is written): the
// returned Call gives a decoder-side helper access to Member, Get and Resolve with
// the same bounds as a reconstruction.
func (e *Env) Open(id SHA) (*Call, error) {
	rep, err := e.load(id, 1, nil)
	if err != nil {
		return nil, err
	}
	return e.call(id, rep, 1, nil, nil), nil
}

// Child opens the child representation of derived member i without
// reconstructing anything, at the next nesting depth.
func (c *Call) Child(i int) (*Call, error) {
	if i < 0 || i >= len(c.Rep.Members) || c.Rep.Members[i].Role != Derived {
		return nil, corrupt("member %d is not derived", i)
	}
	id := c.Rep.Members[i].Child
	rep, err := c.env.load(id, c.depth+1, c.path)
	if err != nil {
		return nil, err
	}
	return c.env.call(id, rep, c.depth+1, c.path, nil), nil
}
