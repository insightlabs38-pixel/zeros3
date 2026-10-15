package repz3

import (
	"bytes"
	"fmt"
	"sort"
)

// Catalog is the laboratory model of the global native-identity accelerator:
//
//	native key -> owning record sections -> SHA-256
//
// It is derived state. The canonical truth is the record sections that live
// representations reference; the catalog holds nothing else, can be discarded at
// any time and is rebuilt from the set of live representation roots (Rebuild).
// A representation never needs it to be rebuilt (Call.Resolve searches the
// representation's own sections), so a stale or missing entry cannot corrupt
// storage: it only costs a slower or missed lookup. Ownership is reference
// counted from the live roots: a mapping disappears with the last root whose
// representation tree lists a section that holds it. The serialized
// representation contains no catalog state.
type Catalog struct {
	src    Source
	env    *Env
	domain func(*Rep, Index) string

	roots map[SHA][]SHA         // live root -> the representations of its tree that own sections
	reps  map[SHA]*catRep       // representation -> its sections, with the number of roots holding it
	secs  map[SHA]*catSec       // section component -> records and the representations listing it
	idx   map[string][]catEntry // domain || key -> owning sections
}

type catRep struct {
	rep   *Rep
	roots int
}

type catSec struct {
	domain string
	keys   [][]byte
	reps   map[SHA]int
}

type catEntry struct {
	sec    SHA
	target SHA
}

// Owner is one record that maps a key: the section holding it, the SHA-256 it
// names and the representations that list the section.
type Owner struct {
	Section SHA
	Target  SHA
	Reps    []SHA
}

// DefaultDomain separates native identities by representation kind and key width.
func DefaultDomain(rep *Rep, x Index) string { return fmt.Sprintf("k%d/w%d", rep.Kind, x.KeyWidth) }

// NewCatalog returns an empty catalog over src. domain (nil = DefaultDomain)
// names the key namespace of a section, so unrelated native hashes of the same
// width never conflict.
func NewCatalog(src Source, domain func(*Rep, Index) string) *Catalog {
	if domain == nil {
		domain = DefaultDomain
	}
	return &Catalog{src: src, env: &Env{Src: src}, domain: domain,
		roots: map[SHA][]SHA{}, reps: map[SHA]*catRep{}, secs: map[SHA]*catSec{}, idx: map[string][]catEntry{}}
}

// Rebuild is the restart path: a fresh catalog from the live roots only.
func Rebuild(src Source, domain func(*Rep, Index) string, roots []SHA) (*Catalog, error) {
	c := NewCatalog(src, domain)
	for _, r := range roots {
		if err := c.Ingest(r); err != nil {
			return nil, fmt.Errorf("rebuilding from root %x: %w", r[:6], err)
		}
	}
	return c, nil
}

func (c *Catalog) tree(root SHA) (map[SHA]*Rep, error) {
	out := map[SHA]*Rep{}
	var visit func(id SHA, depth int, path []SHA) error
	visit = func(id SHA, depth int, path []SHA) error {
		if _, ok := out[id]; ok {
			return nil
		}
		rep, err := c.env.load(id, depth, path)
		if err != nil {
			return err
		}
		out[id] = rep
		p := append(append([]SHA(nil), path...), id)
		for i := range rep.Members {
			if rep.Members[i].Role == Derived {
				if err := visit(rep.Members[i].Child, depth+1, p); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return out, visit(root, 1, nil)
}

// Ingest makes root a live root: every record section its representation tree
// lists is verified and read once, and its records become lookups. A missing or
// corrupt representation or section fails the whole call and leaves the catalog
// unchanged. Ingesting a live root again changes nothing.
func (c *Catalog) Ingest(root SHA) error {
	if _, ok := c.roots[root]; ok {
		return nil
	}
	reps, err := c.tree(root)
	if err != nil {
		return err
	}
	// read every new section before touching the catalog
	loaded := map[SHA]*catSec{}
	type rec struct {
		sec    SHA
		key    []byte
		target SHA
	}
	var recs []rec
	for id, rep := range reps {
		if len(rep.Indexes) == 0 {
			continue
		}
		_ = id
		for _, x := range rep.Indexes {
			if _, ok := c.secs[x.Ref]; ok {
				continue
			}
			if _, ok := loaded[x.Ref]; ok {
				continue
			}
			s := &catSec{domain: c.domain(rep, x), reps: map[SHA]int{}}
			loaded[x.Ref] = s
			if err := ScanIndex(c.src, x, func(k []byte, t SHA) error {
				kk := append([]byte(nil), k...)
				s.keys = append(s.keys, kk)
				recs = append(recs, rec{x.Ref, kk, t})
				return nil
			}); err != nil {
				return fmt.Errorf("section %x: %w", x.Ref[:6], err)
			}
		}
	}
	for ref, s := range loaded {
		c.secs[ref] = s
	}
	for _, r := range recs {
		k := c.secs[r.sec].domain + "\x00" + string(r.key)
		c.idx[k] = append(c.idx[k], catEntry{r.sec, r.target})
	}
	var owners []SHA
	for id, rep := range reps {
		if len(rep.Indexes) == 0 {
			continue
		}
		owners = append(owners, id)
		cr := c.reps[id]
		if cr == nil {
			cr = &catRep{rep: rep}
			c.reps[id] = cr
			for _, x := range rep.Indexes {
				c.secs[x.Ref].reps[id]++
			}
		}
		cr.roots++
	}
	sort.Slice(owners, func(i, j int) bool { return bytes.Compare(owners[i][:], owners[j][:]) < 0 })
	c.roots[root] = owners
	return nil
}

// Remove ends root's ownership. Mappings whose last owning section was only
// listed by representations of this root's tree disappear; mappings still owned
// through another live root remain.
func (c *Catalog) Remove(root SHA) {
	owners, ok := c.roots[root]
	if !ok {
		return
	}
	delete(c.roots, root)
	for _, id := range owners {
		cr := c.reps[id]
		cr.roots--
		if cr.roots > 0 {
			continue
		}
		delete(c.reps, id)
		for _, x := range cr.rep.Indexes {
			s := c.secs[x.Ref]
			delete(s.reps, id)
			if len(s.reps) == 0 {
				c.dropSection(x.Ref, s)
			}
		}
	}
}

func (c *Catalog) dropSection(ref SHA, s *catSec) {
	for _, k := range s.keys {
		key := s.domain + "\x00" + string(k)
		es := c.idx[key]
		out := es[:0]
		for _, e := range es {
			if e.sec != ref {
				out = append(out, e)
			}
		}
		if len(out) == 0 {
			delete(c.idx, key)
		} else {
			c.idx[key] = out
		}
	}
	delete(c.secs, ref)
}

// Owners lists the records that map key in domain, one per owning section.
func (c *Catalog) Owners(domain string, key []byte) []Owner {
	var out []Owner
	for _, e := range c.idx[domain+"\x00"+string(key)] {
		o := Owner{Section: e.sec, Target: e.target}
		for r := range c.secs[e.sec].reps {
			o.Reps = append(o.Reps, r)
		}
		sort.Slice(o.Reps, func(i, j int) bool { return bytes.Compare(o.Reps[i][:], o.Reps[j][:]) < 0 })
		out = append(out, o)
	}
	return out
}

// Resolve returns the component a native identity maps to. Several owners that
// agree are one answer; owners that disagree return ErrAmbiguous: a native
// identity is a hint and the catalog never picks between different components.
func (c *Catalog) Resolve(domain string, key []byte) (SHA, bool, error) {
	es := c.idx[domain+"\x00"+string(key)]
	if len(es) == 0 {
		return SHA{}, false, nil
	}
	t := es[0].target
	for _, e := range es[1:] {
		if e.target != t {
			return SHA{}, false, fmt.Errorf("%w: %x", ErrAmbiguous, key[:min(len(key), 6)])
		}
	}
	return t, true, nil
}

// InSection reports whether the section component ref holds a record for key.
func (c *Catalog) InSection(domain string, key []byte, ref SHA) bool {
	for _, e := range c.idx[domain+"\x00"+string(key)] {
		if e.sec == ref {
			return true
		}
	}
	return false
}

// Known reports whether any live section maps key (no conflict check).
func (c *Catalog) Known(domain string, key []byte) bool {
	return len(c.idx[domain+"\x00"+string(key)]) > 0
}

// Mappings is the canonical dump of the accelerator: key -> the distinct
// targets, sorted. Two catalogs with equal Mappings answer every lookup alike.
func (c *Catalog) Mappings() map[string][]SHA {
	out := make(map[string][]SHA, len(c.idx))
	for k, es := range c.idx {
		seen := map[SHA]bool{}
		var ts []SHA
		for _, e := range es {
			if !seen[e.target] {
				seen[e.target] = true
				ts = append(ts, e.target)
			}
		}
		sort.Slice(ts, func(i, j int) bool { return bytes.Compare(ts[i][:], ts[j][:]) < 0 })
		out[k] = ts
	}
	return out
}

// Sections returns the record sections a live representation lists (nil when the
// representation is not an owner in this catalog).
func (c *Catalog) Sections(rep SHA) []Index {
	if cr := c.reps[rep]; cr != nil {
		return append([]Index(nil), cr.rep.Indexes...)
	}
	return nil
}

// Entries is the number of distinct keys, Records the number of (key, section)
// records held, Sections the number of distinct section components.
func (c *Catalog) Entries() int   { return len(c.idx) }
func (c *Catalog) NSections() int { return len(c.secs) }
func (c *Catalog) Records() (n int) {
	for _, es := range c.idx {
		n += len(es)
	}
	return n
}
