package semcore

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// A Recipe reproduces one original file from literals and component spans:
//
//	file = concat( LIT bytes | REF(component, off, len) ... )
//
// It carries the file length and SHA-256; replay fails closed on any mismatch.
const RecipeMagic = "ZSR1"

const (
	StepLit  = 1
	StepRef  = 2 // whole component payload
	StepSpan = 3 // component payload range
)

type Step struct {
	Kind     uint8
	Lit      []byte
	CID      ID
	Off, Len uint64
	// Fmt is an optional report label ("tensor:name"); it is not serialized.
	Label string
}

type Recipe struct {
	Size  int64
	SHA   ID
	Steps []Step
}

// Marshal serializes the recipe.
func (r *Recipe) Marshal() []byte {
	b := append([]byte(nil), RecipeMagic...)
	b = binary.AppendUvarint(b, uint64(r.Size))
	b = append(b, r.SHA[:]...)
	b = binary.AppendUvarint(b, uint64(len(r.Steps)))
	for i := range r.Steps {
		s := &r.Steps[i]
		b = append(b, s.Kind)
		switch s.Kind {
		case StepLit:
			b = binary.AppendUvarint(b, uint64(len(s.Lit)))
			b = append(b, s.Lit...)
		case StepRef:
			b = append(b, s.CID[:]...)
		case StepSpan:
			b = append(b, s.CID[:]...)
			b = binary.AppendUvarint(b, s.Off)
			b = binary.AppendUvarint(b, s.Len)
		}
	}
	return b
}

// MaxSteps bounds recipe size claims.
const MaxSteps = 1 << 22

// ParseRecipe validates framing and bounds every claimed length.
func ParseRecipe(b []byte) (*Recipe, error) {
	if len(b) < 4 || string(b[:4]) != RecipeMagic {
		return nil, ErrCorrupt
	}
	pos := 4
	u := func() (uint64, error) {
		v, n := binary.Uvarint(b[pos:])
		if n <= 0 {
			return 0, errShort
		}
		pos += n
		return v, nil
	}
	r := &Recipe{}
	sz, err := u()
	if err != nil || sz > 1<<50 || len(b)-pos < 32 {
		return nil, ErrCorrupt
	}
	r.Size = int64(sz)
	copy(r.SHA[:], b[pos:])
	pos += 32
	ns, err := u()
	if err != nil || ns > MaxSteps || ns > uint64(len(b)) {
		return nil, ErrCorrupt
	}
	var total uint64
	for i := uint64(0); i < ns; i++ {
		if pos >= len(b) {
			return nil, errShort
		}
		s := Step{Kind: b[pos]}
		pos++
		switch s.Kind {
		case StepLit:
			l, err := u()
			if err != nil || l > uint64(len(b)-pos) {
				return nil, ErrCorrupt
			}
			s.Lit = b[pos : pos+int(l)]
			pos += int(l)
			total += l
		case StepRef, StepSpan:
			if len(b)-pos < 32 {
				return nil, errShort
			}
			copy(s.CID[:], b[pos:])
			pos += 32
			if s.Kind == StepSpan {
				if s.Off, err = u(); err != nil {
					return nil, err
				}
				if s.Len, err = u(); err != nil {
					return nil, err
				}
				total += s.Len
			}
		default:
			return nil, ErrCorrupt
		}
		if total > 1<<50 {
			return nil, ErrCorrupt
		}
		r.Steps = append(r.Steps, s)
	}
	if pos != len(b) {
		return nil, fmt.Errorf("%w: trailing bytes", ErrCorrupt)
	}
	return r, nil
}

var ErrMismatch = errors.New("semcore: reconstructed length or SHA-256 mismatch")

// Replay writes the exact original file to w using only the store and the recipe,
// then proves length and SHA-256. cacheBytes bounds retained decoded components.
func (s *Store) Replay(r *Recipe, w io.Writer, cacheBytes int64) error {
	rs := &resolver{budget: MaxOutput * 4, active: map[ID]bool{}, cache: map[ID][]byte{}, hold: cacheBytes}
	return s.replayInto(r, w, rs, 0)
}

func (s *Store) replayInto(r *Recipe, w io.Writer, rs *resolver, depth int) error {
	h := sha256.New()
	var n int64
	var held int64
	cacheBytes := rs.hold
	if cacheBytes == 0 {
		cacheBytes = 1 << 62
	}
	for i := range r.Steps {
		st := &r.Steps[i]
		var piece []byte
		switch st.Kind {
		case StepLit:
			piece = st.Lit
		default:
			p, err := s.payloadCached(st.CID, rs, depth)
			if err != nil {
				return err
			}
			held += int64(len(p))
			if st.Kind == StepRef {
				piece = p
			} else {
				if st.Off > uint64(len(p)) || st.Len > uint64(len(p))-st.Off {
					return fmt.Errorf("%w: span out of range", ErrCorrupt)
				}
				piece = p[st.Off : st.Off+st.Len]
			}
			if held > cacheBytes { // drop decoded cache; components are re-derived on demand
				rs.cache = map[ID][]byte{}
				held = 0
			}
		}
		if n+int64(len(piece)) > r.Size {
			return ErrMismatch
		}
		h.Write(piece)
		if k, err := w.Write(piece); err != nil {
			return err
		} else if k != len(piece) {
			return io.ErrShortWrite
		}
		n += int64(len(piece))
	}
	var got ID
	h.Sum(got[:0])
	if n != r.Size || got != r.SHA {
		return ErrMismatch
	}
	return nil
}

// Refs lists every component the recipe names directly.
func (r *Recipe) Refs() []ID {
	var out []ID
	for i := range r.Steps {
		if r.Steps[i].Kind != StepLit {
			out = append(out, r.Steps[i].CID)
		}
	}
	return out
}

// Builder accumulates steps and merges adjacent literals.
type Builder struct{ R Recipe }

func (b *Builder) Lit(p []byte) {
	if len(p) == 0 {
		return
	}
	if n := len(b.R.Steps); n > 0 && b.R.Steps[n-1].Kind == StepLit {
		b.R.Steps[n-1].Lit = append(b.R.Steps[n-1].Lit, p...)
		return
	}
	b.R.Steps = append(b.R.Steps, Step{Kind: StepLit, Lit: append([]byte(nil), p...)})
}

func (b *Builder) Ref(c ID, label string) {
	b.R.Steps = append(b.R.Steps, Step{Kind: StepRef, CID: c, Label: label})
}

func (b *Builder) Span(c ID, off, n uint64, label string) {
	b.R.Steps = append(b.R.Steps, Step{Kind: StepSpan, CID: c, Off: off, Len: n, Label: label})
}

// PutRecipe persists the serialized recipe under its original file digest.
func (s *Store) PutRecipe(r *Recipe) error { return s.B.PutRecipe(r.SHA, r.Marshal()) }

// GetRecipe loads and parses a recipe by original file digest.
func (s *Store) GetRecipe(sha ID) (*Recipe, error) {
	b, err := s.B.GetRecipe(sha)
	if err != nil {
		return nil, err
	}
	return ParseRecipe(b)
}
