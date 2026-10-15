// Package composez3 is the Z3-19 experimental composition coordinator: an outer
// exact representation (here the Z3-11/17 tar+gzip plan) exposes a stable member
// payload; a bounded, explicitly selected inner transform (executable reference
// normalization, SQLite page tree, CAR block set) represents that payload; both
// recipes stay independently exact and the shared SHA-256 components are charged once.
//
// It is deliberately small. There is no plugin registry, no recursive detector and
// no format API: three inner kinds are a switch, detection is a magic-number sniff of
// one member payload, and depth is a constant (outer = 1, inner = 2; MaxDepth).
// Nothing here is a proposed production contract.
//
// Composition record (the only bytes the coordinator adds to the outer plan; charged
// as the plan's index bytes):
//
//	"CMP1" | depth (1 B) | n | n × [ outer piece index | kind (1 B) | prefix length, prefix bytes | pad | inner recipe SHA-256 (32 B) ]
//
// A substituted outer piece keeps its place, digest and length in the outer recipe
// (the outer transform still names the payload and verifies it), but its bytes are
// not stored (lab.Piece.Derived): at rebuild time the payload is produced by the
// inner recipe — itself a stored component — and the stored inner components,
// framed by the record's prefix (a Git object header) and `pad` tar block-padding
// zeros, and checked against the outer digest.
package composez3

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"zeros3-z3/lab"
)

// MaxDepth bounds nesting: the outer representation is depth 1, an inner transform depth 2.
const MaxDepth = 2

var (
	ErrDepth   = errors.New("composez3: nesting depth exceeded")
	ErrCorrupt = errors.New("composez3: corrupt composition record")
)

// Kind selects the inner transform.
type Kind byte

const (
	KindExec   Kind = 1 // ELF / PE: binref reference normalization (Z3-12/13)
	KindSQLite Kind = 2 // sqlitez3 page tree (Z3-18)
	KindCAR    Kind = 3 // carz3 block set (Z3-14)
)

func (k Kind) String() string {
	switch k {
	case KindExec:
		return "exec"
	case KindSQLite:
		return "sqlite"
	case KindCAR:
		return "car"
	}
	return fmt.Sprintf("kind%d", byte(k))
}

// Class labels used for the per-row accounting split.
const (
	ClassPayload = "inner-payload" // inner components carrying content (normalized regions, pages, blocks)
	ClassMeta    = "inner-meta"    // inner non-payload streams / nodes
	ClassRecipe  = "inner-recipe"  // the serialised inner recipe object
)

type Getter = func(lab.ID) ([]byte, error)

// Inner is one exact nested representation of a payload: a serialised recipe and
// the components reachable through it.
type Inner struct {
	Kind   Kind
	Recipe []byte
	Comps  []*lab.Comp
	Detail map[string]any
}

// Detect sniffs the first bytes of a payload. It never scans further.
func Detect(b []byte) (Kind, bool) {
	switch {
	case len(b) >= 64 && bytes.HasPrefix(b, []byte("\x7fELF")):
		return KindExec, true
	case len(b) >= 1024 && b[0] == 'M' && b[1] == 'Z':
		return KindExec, true
	case len(b) >= 512 && bytes.HasPrefix(b, []byte("SQLite format 3\x00")):
		return KindSQLite, true
	case len(b) >= 64 && b[0] < 0x80 && (b[1] == 0xa2 || b[1] == 0xa1): // CARv1 header (varint length, DAG-CBOR map(2)) or the CARv2 pragma (map(1))
		return KindCAR, true
	}
	return 0, false
}

// Encode builds the inner representation of payload and proves it: the recipe is
// used (not any analysis state) with the components to rebuild the payload, and the
// result must equal it. An inner transform that does not apply or fails returns an
// error and the caller leaves the payload to the outer representation.
func Encode(k Kind, payload []byte) (*Inner, error) {
	var in *Inner
	var err error
	switch k {
	case KindExec:
		in, err = encodeExec(payload)
	case KindSQLite:
		in, err = encodeSQLite(payload)
	case KindCAR:
		in, err = encodeCAR(payload)
	default:
		return nil, fmt.Errorf("composez3: unknown kind %d", k)
	}
	if err != nil {
		return nil, err
	}
	store := map[lab.ID][]byte{}
	for _, c := range in.Comps {
		b, e := c.Data()
		if e != nil {
			return nil, e
		}
		store[c.ID] = b
	}
	var out bytes.Buffer
	if err := rebuildInner(k, &out, in.Recipe, func(id lab.ID) ([]byte, error) {
		if b, ok := store[id]; ok {
			return b, nil
		}
		return nil, fmt.Errorf("composez3: inner component %x not stored", id[:6])
	}); err != nil {
		return nil, fmt.Errorf("composez3: %v inner rebuild: %w", k, err)
	}
	if !bytes.Equal(out.Bytes(), payload) {
		return nil, fmt.Errorf("composez3: %v inner rebuild differs from payload", k)
	}
	return in, nil
}

func rebuildInner(k Kind, w io.Writer, recipe []byte, get Getter) error {
	switch k {
	case KindExec:
		return rebuildExec(w, recipe, get)
	case KindSQLite:
		return rebuildSQLite(w, recipe, get)
	case KindCAR:
		return rebuildCAR(w, recipe, get)
	}
	return fmt.Errorf("composez3: unknown kind %d", k)
}

// Options select what Compose substitutes.
type Options struct {
	Depth   int    // depth of the outer plan (default 1)
	MinSize int64  // smallest member body considered (default 64 KiB)
	Kinds   []Kind // allowed inner kinds (nil = all)
}

// Report summarises one composition.
type Report struct {
	Considered, Substituted, Fallback int
	ByKind                            map[Kind]int
	PayloadBytes                      int64 // bytes no longer stored by the outer plan
	Record                            int
	Notes                             []string
}

type sub struct {
	idx  int
	kind Kind
	pre  string // bytes before the nested object (a Git object header), at most maxPre
	pad  int    // zero bytes after it (tar block padding), at most 511
	rid  lab.ID
}

const maxPre = 32

func marshalRecord(depth int, subs []sub) []byte {
	b := append([]byte("CMP1"), byte(depth))
	b = binary.AppendUvarint(b, uint64(len(subs)))
	for _, s := range subs {
		b = binary.AppendUvarint(b, uint64(s.idx))
		b = append(b, byte(s.kind))
		b = binary.AppendUvarint(b, uint64(len(s.pre)))
		b = append(b, s.pre...)
		b = binary.AppendUvarint(b, uint64(s.pad))
		b = append(b, s.rid[:]...)
	}
	return b
}

func parseRecord(b []byte, npieces int) (depth int, subs []sub, err error) {
	if len(b) < 6 || string(b[:4]) != "CMP1" {
		return 0, nil, ErrCorrupt
	}
	depth = int(b[4])
	if depth < 1 || depth >= MaxDepth {
		return 0, nil, fmt.Errorf("%w: depth %d", ErrDepth, depth)
	}
	pos := 5
	uv := func() (uint64, bool) {
		v, n := binary.Uvarint(b[pos:])
		if n <= 0 {
			return 0, false
		}
		pos += n
		return v, true
	}
	n, ok := uv()
	if !ok || n > uint64(len(b)) { // each entry is at least 35 bytes: a count above len(b) is impossible
		return 0, nil, ErrCorrupt
	}
	last := -1
	for i := uint64(0); i < n; i++ {
		idx, ok := uv()
		if !ok || idx >= uint64(npieces) || int(idx) <= last || pos >= len(b) {
			return 0, nil, ErrCorrupt
		}
		last = int(idx)
		k := Kind(b[pos])
		pos++
		pl, ok := uv()
		if !ok || pl > maxPre || uint64(len(b)-pos) < pl {
			return 0, nil, ErrCorrupt
		}
		pre := string(b[pos : pos+int(pl)])
		pos += int(pl)
		pad, ok := uv()
		if !ok || pad > 511 || len(b)-pos < 32 {
			return 0, nil, ErrCorrupt
		}
		s := sub{idx: int(idx), kind: k, pre: pre, pad: int(pad)}
		copy(s.rid[:], b[pos:])
		pos += 32
		subs = append(subs, s)
	}
	if pos != len(b) {
		return 0, nil, ErrCorrupt
	}
	return depth, subs, nil
}

// Compose substitutes the inner representations for the detected member payloads of
// an exact outer plan. The outer plan must list every piece (no Via pieces), because
// the composed rebuild addresses outer pieces by position after the recipe round trip.
func Compose(outer *lab.Plan, opt Options) (*lab.Plan, *Report, error) {
	if opt.Depth == 0 {
		opt.Depth = 1
	}
	if opt.Depth+1 > MaxDepth {
		return nil, nil, ErrDepth
	}
	if opt.MinSize == 0 {
		opt.MinSize = 64 << 10
	}
	if outer.Rebuild == nil || outer.Oracle {
		return nil, nil, errors.New("composez3: the outer plan is not an exact representation")
	}
	rep := &Report{ByKind: map[Kind]int{}}
	n := len(outer.Pieces)
	pieces := append([]lab.Piece(nil), outer.Pieces...)
	var subs []sub
	var extra []lab.Piece
	allowed := func(k Kind) bool {
		if opt.Kinds == nil {
			return true
		}
		for _, a := range opt.Kinds {
			if a == k {
				return true
			}
		}
		return false
	}
	done := map[lab.ID]sub{}
	for i, pc := range outer.Pieces {
		if pc.Via {
			return nil, nil, errors.New("composez3: outer plan has Via pieces")
		}
		c := pc.Comp
		if c == nil || (c.Class != "tarpay" && c.Class != "gitobj") || c.Size < opt.MinSize || c.Pad < 0 || c.Pre < 0 || c.Pre > maxPre || int64(c.Pad+c.Pre) >= c.Size {
			continue
		}
		rep.Considered++
		if prev, ok := done[c.ID]; ok { // the same member body twice: one inner representation
			subs = append(subs, sub{idx: i, kind: prev.kind, pre: prev.pre, pad: prev.pad, rid: prev.rid})
			pieces[i].Derived = true
			continue
		}
		body, err := c.Data()
		if err != nil {
			return nil, nil, err
		}
		obj := body[c.Pre : len(body)-c.Pad]
		if !allZero(body[len(body)-c.Pad:]) {
			continue
		}
		k, ok := Detect(obj)
		if !ok || !allowed(k) {
			continue
		}
		in, err := Encode(k, obj)
		if err != nil {
			rep.Fallback++
			rep.Notes = append(rep.Notes, fmt.Sprintf("piece %d %v: %v", i, k, err))
			continue
		}
		rc := &lab.Comp{ID: sha256.Sum256(in.Recipe), Size: int64(len(in.Recipe)), Class: ClassRecipe, Data: bytesData(in.Recipe)}
		s := sub{idx: i, kind: k, pre: string(body[:c.Pre]), pad: c.Pad, rid: rc.ID}
		subs = append(subs, s)
		done[c.ID] = s
		pieces[i].Derived = true
		extra = append(extra, lab.Piece{Comp: rc, Via: true, Len: rc.Size})
		for _, ic := range in.Comps {
			extra = append(extra, lab.Piece{Comp: ic, Via: true, Len: ic.Size})
		}
		rep.Substituted++
		rep.ByKind[k]++
		rep.PayloadBytes += c.Size
	}
	if len(subs) == 0 {
		return outer, rep, nil
	}
	rec := marshalRecord(opt.Depth, subs)
	rep.Record = len(rec)
	detail := map[string]any{"composition_record": rec}
	for k, v := range outer.Detail {
		detail[k] = v
	}
	p := &lab.Plan{Pieces: append(pieces, extra...), MetaBytes: outer.MetaBytes, IndexBytes: outer.IndexBytes + int64(len(rec)), Detail: detail}
	p.Rebuild = func(w io.Writer, get Getter) error { return rebuildComposed(rec, outer, n, p.Pieces, w, get) }
	return p, rep, nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func bytesData(b []byte) func() ([]byte, error) { return func() ([]byte, error) { return b, nil } }

// Audit reports accidental duplicate charging in a composed plan: a derived payload
// that is also stored by some piece, or one component ID stored under two roles.
func Audit(p *lab.Plan) []string {
	var out []string
	stored := map[lab.ID]string{}
	for _, pc := range p.Pieces {
		if pc.Comp == nil || pc.Derived {
			continue
		}
		if prev, ok := stored[pc.Comp.ID]; ok && prev != pc.Comp.Class {
			out = append(out, fmt.Sprintf("component %x stored as %q and %q", pc.Comp.ID[:6], prev, pc.Comp.Class))
		}
		stored[pc.Comp.ID] = pc.Comp.Class
	}
	for _, pc := range p.Pieces {
		if pc.Comp != nil && pc.Derived {
			if cl, ok := stored[pc.Comp.ID]; ok {
				out = append(out, fmt.Sprintf("derived payload %x is also stored (%s)", pc.Comp.ID[:6], cl))
			}
		}
	}
	return out
}

// rebuildComposed is the whole composed reconstruction: it needs the serialised
// composition record, the outer plan's rebuild (which reads the round-tripped outer
// piece list `pieces[:n]`) and the stored components.
func rebuildComposed(rec []byte, outer *lab.Plan, n int, pieces []lab.Piece, w io.Writer, get Getter) error {
	_, ss, err := parseRecord(rec, n)
	if err != nil {
		return err
	}
	if len(pieces) < n {
		return fmt.Errorf("%w: %d pieces, outer needs %d", ErrCorrupt, len(pieces), n)
	}
	outer.Pieces = pieces[:n]
	byID := map[lab.ID]sub{}
	for _, s := range ss {
		if outer.Pieces[s.idx].Comp == nil {
			return ErrCorrupt
		}
		byID[outer.Pieces[s.idx].Comp.ID] = s
	}
	cache := map[lab.ID][]byte{}
	return outer.Rebuild(w, func(id lab.ID) ([]byte, error) {
		s, ok := byID[id]
		if !ok {
			return get(id)
		}
		if b, ok := cache[id]; ok {
			return b, nil
		}
		recipe, err := get(s.rid)
		if err != nil {
			return nil, err
		}
		if sha256.Sum256(recipe) != s.rid {
			return nil, fmt.Errorf("%w: inner recipe does not match its digest", ErrCorrupt)
		}
		var buf bytes.Buffer
		buf.WriteString(s.pre)
		if err := rebuildInner(s.kind, &buf, recipe, get); err != nil {
			return nil, err
		}
		buf.Write(make([]byte, s.pad))
		if sha256.Sum256(buf.Bytes()) != id {
			return nil, fmt.Errorf("%w: inner rebuild does not match the outer digest", ErrCorrupt)
		}
		cache[id] = buf.Bytes()
		return buf.Bytes(), nil
	})
}

// RebuildInner reconstructs an inner object from its serialised recipe and the stored
// components (exported for the read measurements).
func RebuildInner(k Kind, w io.Writer, recipe []byte, get Getter) error {
	return rebuildInner(k, w, recipe, get)
}

// InnerPlan is the inner transform alone, as a plan for a standalone object: the
// recipe is the single listed component and the inner components hang off it (Via).
func InnerPlan(k Kind, payload []byte) (*lab.Plan, *Inner, error) {
	in, err := Encode(k, payload)
	if err != nil {
		return nil, nil, err
	}
	rc := &lab.Comp{ID: sha256.Sum256(in.Recipe), Size: int64(len(in.Recipe)), Class: ClassRecipe, Data: bytesData(in.Recipe)}
	p := &lab.Plan{Pieces: []lab.Piece{{Comp: rc, Len: rc.Size}}, Detail: in.Detail}
	for _, c := range in.Comps {
		p.Pieces = append(p.Pieces, lab.Piece{Comp: c, Via: true, Len: c.Size})
	}
	p.Rebuild = func(w io.Writer, get Getter) error {
		if len(p.Pieces) == 0 || p.Pieces[0].Comp == nil {
			return ErrCorrupt
		}
		recipe, err := get(p.Pieces[0].Comp.ID)
		if err != nil {
			return err
		}
		return rebuildInner(k, w, recipe, get)
	}
	return p, in, nil
}
