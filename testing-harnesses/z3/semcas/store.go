package semcas

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Limits for closure resolution; hostile records fail closed.
const (
	MaxDepth  = 4
	MaxFanout = 1024
	RecMagic  = "ZSC1"
)

var (
	ErrMissing = errors.New("semcas: missing component")
	ErrCorrupt = errors.New("semcas: corrupt record")
	ErrDepth   = errors.New("semcas: dependency depth/fanout/cycle limit")
)

// Record is one typed component. A Raw record has its payload in a blob keyed by
// payload digest (so byte-identical payloads of different types share bytes); a
// Derived record stores only parents, a pipeline and an exact residual.
type Record struct {
	CID        ID
	Desc       Desc
	PayloadLen int64
	Payload    ID // SHA-256 of the canonical payload
	Parents    []ID
	Ops        []Op
	Patches    []Patch
}

// Derived reports whether the payload is produced from parents.
func (r *Record) Derived() bool { return len(r.Ops) > 0 }

// Marshal is the self-authenticating serialized record.
func (r *Record) Marshal() []byte {
	b := append([]byte(nil), RecMagic...)
	b = append(b, r.Desc.Marshal()...)
	b = binary.AppendUvarint(b, uint64(r.PayloadLen))
	b = append(b, r.Payload[:]...)
	b = binary.AppendUvarint(b, uint64(len(r.Parents)))
	for _, p := range r.Parents {
		b = append(b, p[:]...)
	}
	b = binary.AppendUvarint(b, uint64(len(r.Ops)))
	for i := range r.Ops {
		b = r.Ops[i].marshal(b)
	}
	if len(r.Ops) > 0 {
		b = append(b, marshalPatches(r.Patches)...)
	}
	return b
}

// ParseRecord validates framing and recomputes the CID from the content.
func ParseRecord(b []byte) (*Record, error) {
	if len(b) < 4 || string(b[:4]) != RecMagic {
		return nil, ErrCorrupt
	}
	r := &Record{}
	d, n, err := ParseDesc(b[4:])
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	r.Desc = d
	pos := 4 + n
	u := func() (uint64, error) {
		v, n := binary.Uvarint(b[pos:])
		if n <= 0 {
			return 0, errShort
		}
		pos += n
		return v, nil
	}
	pl, err := u()
	if err != nil || pl > MaxOutput || len(b)-pos < 32 {
		return nil, ErrCorrupt
	}
	r.PayloadLen = int64(pl)
	copy(r.Payload[:], b[pos:])
	pos += 32
	np, err := u()
	if err != nil || np > MaxFanout || uint64(len(b)-pos) < np*32 {
		return nil, ErrDepth
	}
	for i := uint64(0); i < np; i++ {
		var p ID
		copy(p[:], b[pos:])
		pos += 32
		r.Parents = append(r.Parents, p)
	}
	no, err := u()
	if err != nil || no > 16 {
		return nil, ErrCorrupt
	}
	for i := uint64(0); i < no; i++ {
		o, n, err := parseOp(b[pos:])
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		pos += n
		r.Ops = append(r.Ops, o)
	}
	if no > 0 {
		ps, n, err := parsePatches(b[pos:], pl)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		pos += n
		r.Patches = ps
		if len(r.Parents) == 0 && r.Ops[0].Kind != OpRecipe {
			return nil, ErrCorrupt
		}
	} else if np != 0 {
		return nil, ErrCorrupt
	}
	if pos != len(b) {
		return nil, fmt.Errorf("%w: trailing bytes", ErrCorrupt)
	}
	if want := r.Desc.PayloadLen(); want >= 0 && want != r.PayloadLen {
		return nil, fmt.Errorf("%w: descriptor implies %d bytes, record %d", ErrCorrupt, want, r.PayloadLen)
	}
	r.CID = CID(&r.Desc, r.PayloadLen, r.Payload)
	return r, nil
}

// Store is a directory of blobs/ (payloads by byte digest) and comps/ (records by CID).
// Indexes are in-memory caches rebuilt by scanning comps/; there is no authoritative
// global namespace.
type Store struct {
	Dir string
	mu  sync.Mutex
	rec map[ID]*Record
	// derived indexes (rebuildable)
	byPayload map[ID][]ID
	byElems   map[uint64][]ID
	// Reads counts physical blob bytes read (for amplification measurements).
	Reads int64
	Mat   int64 // bytes materialized through derivations
	// Written counts blob bytes newly written (new physical payload).
	Written int64
}

// Open creates dir if needed and rebuilds the in-memory indexes from records.
func Open(dir string) (*Store, error) {
	s := &Store{Dir: dir, rec: map[ID]*Record{}, byPayload: map[ID][]ID{}, byElems: map[uint64][]ID{}}
	for _, sub := range []string{"blobs", "comps", "recipes"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	ents, err := os.ReadDir(filepath.Join(dir, "comps"))
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(dir, "comps", e.Name()))
		if err != nil {
			return nil, err
		}
		r, err := ParseRecord(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if r.CID.String() != e.Name() {
			return nil, fmt.Errorf("%s: record identity mismatch", e.Name())
		}
		s.index(r)
	}
	return s, nil
}

func (s *Store) index(r *Record) {
	s.rec[r.CID] = r
	s.byPayload[r.Payload] = append(s.byPayload[r.Payload], r.CID)
	if r.Desc.Kind == KindTensor {
		if n, err := r.Desc.Elems(); err == nil {
			s.byElems[n] = append(s.byElems[n], r.CID)
		}
	}
}

func (s *Store) blobPath(id ID) string { return filepath.Join(s.Dir, "blobs", id.String()) }

// Has reports whether a record exists.
func (s *Store) Has(c ID) bool { s.mu.Lock(); defer s.mu.Unlock(); return s.rec[c] != nil }

// Rec returns a record.
func (s *Store) Rec(c ID) *Record { s.mu.Lock(); defer s.mu.Unlock(); return s.rec[c] }

// ByPayload lists component IDs whose payload digest is p.
func (s *Store) ByPayload(p ID) []ID { s.mu.Lock(); defer s.mu.Unlock(); return s.byPayload[p] }

// ByElems lists tensor components with n elements.
func (s *Store) ByElems(n uint64) []ID { s.mu.Lock(); defer s.mu.Unlock(); return s.byElems[n] }

// Count returns the number of component records.
func (s *Store) Count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.rec) }

// All returns every record sorted by CID.
func (s *Store) All() []*Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Record, 0, len(s.rec))
	for _, r := range s.rec {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].CID[:], out[j].CID[:]) < 0 })
	return out
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// PutRaw stores a typed payload. The blob is written once per byte digest.
func (s *Store) PutRaw(d Desc, payload []byte) (*Record, error) {
	if want := d.PayloadLen(); want >= 0 && want != int64(len(payload)) {
		return nil, fmt.Errorf("semcas: descriptor implies %d bytes, got %d", want, len(payload))
	}
	r := &Record{Desc: d, PayloadLen: int64(len(payload)), Payload: PayloadSHA(payload)}
	r.CID = CID(&r.Desc, r.PayloadLen, r.Payload)
	if old := s.Rec(r.CID); old != nil {
		return old, nil
	}
	if _, err := os.Stat(s.blobPath(r.Payload)); err != nil {
		if err := writeAtomic(s.blobPath(r.Payload), payload); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.Written += int64(len(payload))
		s.mu.Unlock()
	}
	if err := writeAtomic(filepath.Join(s.Dir, "comps", r.CID.String()), r.Marshal()); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.index(r)
	s.mu.Unlock()
	return r, nil
}

// PutDerived registers a derived record after proving that materializing it reproduces
// payload exactly. The caller supplies the final payload for the proof only.
func (s *Store) PutDerived(d Desc, parents []ID, ops []Op, patches []Patch, payload []byte) (*Record, error) {
	r := &Record{Desc: d, PayloadLen: int64(len(payload)), Payload: PayloadSHA(payload), Parents: parents, Ops: ops, Patches: patches}
	r.CID = CID(&r.Desc, r.PayloadLen, r.Payload)
	if old := s.Rec(r.CID); old != nil {
		return old, nil
	}
	if want := d.PayloadLen(); want >= 0 && want != r.PayloadLen {
		return nil, fmt.Errorf("semcas: descriptor implies %d bytes, got %d", want, len(payload))
	}
	ser := r.Marshal()
	back, err := ParseRecord(ser)
	if err != nil {
		return nil, err
	}
	got, err := s.materialize(back, &resolver{budget: MaxOutput * 4, active: map[ID]bool{}, cache: map[ID][]byte{}}, 0)
	if err != nil {
		return nil, fmt.Errorf("semcas: derivation proof failed: %w", err)
	}
	if !bytes.Equal(got, payload) {
		return nil, errors.New("semcas: derivation proof mismatch")
	}
	if err := writeAtomic(filepath.Join(s.Dir, "comps", r.CID.String()), ser); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.index(r)
	s.mu.Unlock()
	return r, nil
}

type resolver struct {
	hold   int64
	budget int64
	active map[ID]bool
	cache  map[ID][]byte
}

// Payload materializes and authenticates a component.
func (s *Store) Payload(c ID) ([]byte, error) {
	r := s.Rec(c)
	if r == nil {
		return nil, ErrMissing
	}
	return s.materialize(r, &resolver{budget: MaxOutput * 4, active: map[ID]bool{}, cache: map[ID][]byte{}}, 0)
}

// PayloadWith is Payload sharing a decoded cache (bounded by caller).
func (s *Store) payloadCached(c ID, rs *resolver, depth int) ([]byte, error) {
	if b, ok := rs.cache[c]; ok {
		return b, nil
	}
	r := s.Rec(c)
	if r == nil {
		return nil, fmt.Errorf("%w: %s", ErrMissing, c.Short())
	}
	return s.materialize(r, rs, depth)
}

func (s *Store) materialize(r *Record, rs *resolver, depth int) ([]byte, error) {
	if b, ok := rs.cache[r.CID]; ok {
		return b, nil
	}
	if depth > MaxDepth || rs.active[r.CID] {
		return nil, ErrDepth
	}
	rs.active[r.CID] = true
	defer delete(rs.active, r.CID)
	var out []byte
	if !r.Derived() {
		b, err := os.ReadFile(s.blobPath(r.Payload))
		if err != nil {
			return nil, fmt.Errorf("%w: blob %s", ErrMissing, r.Payload.Short())
		}
		s.mu.Lock()
		s.Reads += int64(len(b))
		s.mu.Unlock()
		out = b
	} else if r.Ops[0].Kind == OpRecipe && len(r.Ops) == 1 && len(r.Parents) == 0 {
		rc, err := ParseRecipe(r.Ops[0].Aux)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if err := s.replayInto(rc, &buf, rs, depth+1); err != nil {
			return nil, err
		}
		out = buf.Bytes()
		s.mu.Lock()
		s.Mat += int64(len(out))
		s.mu.Unlock()
	} else {
		in := make([][]byte, len(r.Parents))
		for i, p := range r.Parents {
			b, err := s.payloadCached(p, rs, depth+1)
			if err != nil {
				return nil, err
			}
			in[i] = b
		}
		cur := in
		for i := range r.Ops {
			o, err := r.Ops[i].Apply(cur)
			if err != nil {
				return nil, err
			}
			cur = [][]byte{o}
		}
		out = cur[0]
		if int64(len(out)) != r.PayloadLen {
			return nil, fmt.Errorf("%w: derived length %d != %d", ErrCorrupt, len(out), r.PayloadLen)
		}
		if err := ApplyPatches(out, r.Patches); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.Mat += int64(len(out))
		s.mu.Unlock()
	}
	rs.budget -= int64(len(out))
	if rs.budget < 0 {
		return nil, errBudget
	}
	if int64(len(out)) != r.PayloadLen || sha256.Sum256(out) != r.Payload {
		return nil, fmt.Errorf("%w: payload digest mismatch for %s", ErrCorrupt, r.CID.Short())
	}
	rs.cache[r.CID] = out
	return out, nil
}

// Closure returns the transitive dependency set of c (including c), depth-first.
func (s *Store) Closure(c ID) ([]ID, error) {
	var out []ID
	seen := map[ID]bool{}
	active := map[ID]bool{}
	var walk func(ID, int) error
	walk = func(x ID, d int) error {
		if d > MaxDepth || active[x] {
			return ErrDepth
		}
		r := s.Rec(x)
		if r == nil {
			return ErrMissing
		}
		if seen[x] {
			return nil
		}
		seen[x] = true
		active[x] = true
		defer delete(active, x)
		for _, p := range r.deps() {
			if err := walk(p, d+1); err != nil {
				return err
			}
		}
		out = append(out, x)
		return nil
	}
	return out, walk(c, 0)
}

// PutDerivedProbe proves a derivation like PutDerived but writes nothing and indexes nothing.
func (s *Store) PutDerivedProbe(d Desc, parents []ID, ops []Op, patches []Patch, payload []byte) (*Record, error) {
	r := &Record{Desc: d, PayloadLen: int64(len(payload)), Payload: PayloadSHA(payload), Parents: parents, Ops: ops, Patches: patches}
	r.CID = CID(&r.Desc, r.PayloadLen, r.Payload)
	back, err := ParseRecord(r.Marshal())
	if err != nil {
		return nil, err
	}
	got, err := s.materialize(back, &resolver{budget: MaxOutput * 4, active: map[ID]bool{}, cache: map[ID][]byte{}}, 0)
	if err != nil {
		return nil, err
	}
	if PayloadSHA(got) != r.Payload {
		return nil, ErrCorrupt
	}
	return r, nil
}

// Rederive replaces the physical form of an existing component by a derivation with the
// same semantic identity (the CID depends only on type and content), after proving that
// the derivation reproduces the payload and that no cycle or depth violation results.
// The old blob is deleted only when no other raw record shares its byte digest.
func (s *Store) Rederive(r *Record) error {
	old := s.Rec(r.CID)
	if old == nil || !r.Derived() {
		return ErrMissing
	}
	for _, p := range r.Parents {
		cl, err := s.Closure(p)
		if err != nil {
			return err
		}
		for _, x := range cl {
			if x == r.CID {
				return ErrDepth
			}
		}
	}
	back, err := ParseRecord(r.Marshal())
	if err != nil {
		return err
	}
	got, err := s.materialize(back, &resolver{budget: MaxOutput * 4, active: map[ID]bool{}, cache: map[ID][]byte{}}, 0)
	if err != nil || PayloadSHA(got) != r.Payload {
		return fmt.Errorf("semcas: rederive proof failed: %v", err)
	}
	prev := old
	s.mu.Lock()
	s.rec[r.CID] = back
	s.mu.Unlock()
	// depth of the whole store must stay within limits
	for _, x := range s.All() {
		if _, err := s.Closure(x.CID); err != nil {
			s.mu.Lock()
			s.rec[r.CID] = prev
			s.mu.Unlock()
			return err
		}
	}
	if err := writeAtomic(filepath.Join(s.Dir, "comps", r.CID.String()), back.Marshal()); err != nil {
		return err
	}
	shared := false
	for _, id := range s.ByPayload(r.Payload) {
		if o := s.Rec(id); id != r.CID && !o.Derived() {
			shared = true
		}
	}
	if !shared {
		os.Remove(s.blobPath(r.Payload))
	}
	return nil
}

// deps lists every component this record needs: parents and, for a recipe-valued
// derivation, the components that recipe references.
func (r *Record) deps() []ID {
	out := append([]ID(nil), r.Parents...)
	if len(r.Ops) == 1 && r.Ops[0].Kind == OpRecipe {
		if rc, err := ParseRecipe(r.Ops[0].Aux); err == nil {
			out = append(out, rc.Refs()...)
		}
	}
	return out
}

// Materialize converts a derived component back into a raw blob-backed record (identity is
// unchanged), so another component can be rooted at it.
func (s *Store) Materialize(c ID) error {
	r := s.Rec(c)
	if r == nil {
		return ErrMissing
	}
	if !r.Derived() {
		return nil
	}
	b, err := s.Payload(c)
	if err != nil {
		return err
	}
	if err := writeAtomic(s.blobPath(r.Payload), b); err != nil {
		return err
	}
	raw := &Record{CID: r.CID, Desc: r.Desc, PayloadLen: r.PayloadLen, Payload: r.Payload}
	if err := writeAtomic(filepath.Join(s.Dir, "comps", c.String()), raw.Marshal()); err != nil {
		return err
	}
	s.mu.Lock()
	s.rec[c] = raw
	s.Written += int64(len(b))
	s.mu.Unlock()
	return nil
}
