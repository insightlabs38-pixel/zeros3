package prodshadow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"zeros3-z3/lab"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// ErrAbort is returned by the crash-injection points.
var ErrAbort = errors.New("prodshadow: injected abort")

// Meta is the lab-only shadow metadata store: one checksummed, content-addressed
// root file per published shadow representation. It is outside the production
// journal and manifests; it may be absent, stale, partial or corrupt.
type Meta struct{ Dir string }

func (m *Meta) rootsDir() string { return filepath.Join(m.Dir, "roots") }

// Publish writes the root atomically: temp file, fsync, rename, directory fsync.
// hook (may be nil) is called at "tmp-written" and "renamed"; a non-nil error
// aborts there, leaving exactly what a crash at that point would leave.
func (m *Meta) Publish(r *Root, hook func(point string) error) ([32]byte, error) {
	b, err := r.Marshal()
	if err != nil {
		return [32]byte{}, err
	}
	id := sha256.Sum256(b)
	dir := m.rootsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return id, err
	}
	tmp := filepath.Join(dir, ".tmp-"+hex.EncodeToString(id[:]))
	f, err := os.Create(tmp)
	if err != nil {
		return id, err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return id, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return id, err
	}
	f.Close()
	if hook != nil {
		if err := hook("tmp-written"); err != nil {
			return id, err
		}
	}
	if err := os.Rename(tmp, filepath.Join(dir, hex.EncodeToString(id[:])+".root")); err != nil {
		return id, err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	if hook != nil {
		if err := hook("renamed"); err != nil {
			return id, err
		}
	}
	return id, nil
}

// Load returns every valid published root and one error per rejected file. A root
// file is valid only if its name is the SHA-256 of its bytes and it parses.
func (m *Meta) Load() (roots []*Root, ids [][32]byte, bad map[string]error) {
	bad = map[string]error{}
	ents, _ := os.ReadDir(m.rootsDir())
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".root") { // .tmp-* leftovers are not roots
			continue
		}
		b, err := os.ReadFile(filepath.Join(m.rootsDir(), name))
		if err != nil {
			bad[name] = err
			continue
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:])+".root" != name {
			bad[name] = fmt.Errorf("%w: file name is not the digest of its bytes", ErrDescriptor)
			continue
		}
		r, err := UnmarshalRoot(b)
		if err != nil {
			bad[name] = err
			continue
		}
		roots, ids = append(roots, r), append(ids, sum)
	}
	return
}

// Retire removes a published root (the shadow-root policy decision; production
// does not know about it).
func (m *Meta) Retire(id [32]byte) error {
	return os.Remove(filepath.Join(m.rootsDir(), hex.EncodeToString(id[:])+".root"))
}

// Bytes is the size of all root files.
func (m *Meta) Bytes() (n int64) {
	ents, _ := os.ReadDir(m.rootsDir())
	for _, e := range ents {
		if i, err := e.Info(); err == nil && !e.IsDir() {
			n += i.Size()
		}
	}
	return
}

// Options choose the component mapping.
type Options struct {
	// VirtualAbove: logical components larger than this become virtual (a
	// descriptor over production-CDC chunks); smaller ones are one direct CAS
	// chunk. It is clamped to MaxChunk: a direct chunk above the production
	// record limit would never be packed. 0 means MaxChunk.
	VirtualAbove int64
	// AllVirtual forces every non-empty component through a descriptor
	// (measurement of the "virtual everywhere" alternative).
	AllVirtual bool
}

// StageStats classify what one Stage did to the real CAS.
type StageStats struct {
	Objects, Direct, Virtual                 int
	LogicalBytes                             int64 // exact bytes of every logical object in the bundle
	DirectBytes, VirtualBytes                int64
	ChunksNew, ChunksExistedCDC, ChunksReuse int
	NewBytes, ExistedCDCBytes, ReuseBytes    int64
	DescBytes                                int64 // canonical descriptor bodies charged
}

// Staged is a representation whose components are in the CAS but whose root is
// not yet published.
type Staged struct {
	Rep   repz3.SHA
	Descs map[[32]byte]*Descriptor
	Objs  []repz3.SHA
	Stats StageStats
}

// Shadow builds shadow representations over a Bridge.
type Shadow struct {
	Br   *Bridge
	Src  *Source
	Meta *Meta
	Opt  Options

	mu    sync.Mutex
	known map[[32]byte]bool // chunks this Shadow has written or adopted
}

func New(br *Bridge, meta *Meta, opt Options, cacheChunks int) *Shadow {
	if opt.VirtualAbove <= 0 || opt.VirtualAbove > MaxChunk {
		opt.VirtualAbove = MaxChunk
	}
	return &Shadow{Br: br, Src: NewSource(br, cacheChunks), Meta: meta, Opt: opt, known: map[[32]byte]bool{}}
}

type pending struct {
	sha  [32]byte
	data []byte
}

func (sh *Shadow) flush(batch []pending, st *StageStats) error {
	if len(batch) == 0 {
		return nil
	}
	chunks := make([][]byte, len(batch))
	for i, p := range batch {
		chunks[i] = p.data
	}
	res, err := sh.Br.PutBatch(chunks)
	if err != nil {
		return err
	}
	if len(res) != len(batch) {
		return fmt.Errorf("prodshadow: putbatch answered %d of %d", len(res), len(batch))
	}
	for i, r := range res {
		if r.SHA != hex.EncodeToString(batch[i].sha[:]) {
			return fmt.Errorf("prodshadow: CAS digest mismatch for chunk %d", i)
		}
		n := int64(len(batch[i].data))
		if r.Existed {
			st.ChunksExistedCDC++
			st.ExistedCDCBytes += n
		} else {
			st.ChunksNew++
			st.NewBytes += n
		}
	}
	return nil
}

// Stage writes the bundle's objects (children first, representation object last)
// through the production CAS. After abortAfter objects (>0) it stops with
// ErrAbort, leaving a partial shadow exactly as a crash would.
func (sh *Shadow) Stage(b *kinds.Bundle, abortAfter int) (*Staged, error) {
	out := &Staged{Rep: b.ID, Descs: map[[32]byte]*Descriptor{}}
	st := &out.Stats
	var batch []pending
	var batchBytes int
	seen := map[[32]byte]bool{}
	send := func(sha [32]byte, data []byte) error {
		sh.mu.Lock()
		dup := sh.known[sha]
		sh.known[sha] = true
		sh.mu.Unlock()
		if dup {
			st.ChunksReuse++
			st.ReuseBytes += int64(len(data))
			return nil
		}
		batch = append(batch, pending{sha, data})
		if batchBytes += len(data); batchBytes >= 8<<20 || len(batch) >= 256 {
			err := sh.flush(batch, st)
			batch, batchBytes = nil, 0
			return err
		}
		return nil
	}
	var objs [][]byte
	var collect func(b *kinds.Bundle)
	collect = func(b *kinds.Bundle) {
		for _, c := range b.Children {
			collect(c)
		}
		objs = append(objs, b.Objects...)
		objs = append(objs, b.Raw)
	}
	collect(b)
	count := 0
	for _, o := range objs {
		id := sha256.Sum256(o)
		if seen[id] {
			continue
		}
		seen[id] = true
		if abortAfter > 0 && count >= abortAfter {
			if err := sh.flush(batch, st); err != nil {
				return nil, err
			}
			return out, ErrAbort
		}
		count++
		out.Objs = append(out.Objs, id)
		st.Objects++
		st.LogicalBytes += int64(len(o))
		switch {
		case len(o) == 0:
		case int64(len(o)) <= sh.Opt.VirtualAbove && !sh.Opt.AllVirtual:
			st.Direct++
			st.DirectBytes += int64(len(o))
			if err := send(id, o); err != nil {
				return nil, err
			}
		default:
			d := &Descriptor{Size: int64(len(o))}
			err := lab.ChunkCDC(bytes.NewReader(o), func(_ int64, c []byte) error {
				cp := append([]byte(nil), c...)
				d.Refs = append(d.Refs, Ref{sha256.Sum256(cp), uint32(len(cp))})
				return send(sha256.Sum256(cp), cp)
			})
			if err != nil {
				return nil, err
			}
			if err := d.Validate(); err != nil {
				return nil, err
			}
			st.Virtual++
			st.VirtualBytes += int64(len(o))
			st.DescBytes += int64(len(d.Append(nil)))
			out.Descs[id] = d
		}
	}
	if err := sh.flush(batch, st); err != nil {
		return nil, err
	}
	sh.Src.AddDescriptors(out.Descs)
	return out, nil
}

// Env is a repz3 decoding environment over the production-backed Source.
func (sh *Shadow) Env() *repz3.Env { return kinds.NewEnv(sh.Src) }

// Publish makes a staged representation visible: it collects the descriptors of
// every virtual object in the representation's closure (including components
// shared with earlier revisions) and atomically writes the root.
func (sh *Shadow) Publish(key string, objSHA [32]byte, objSize int64, rep repz3.SHA, hook func(string) error) ([32]byte, error) {
	reach, err := sh.Env().Reachable(rep)
	if err != nil {
		return [32]byte{}, err
	}
	r := &Root{Key: key, ObjSHA: objSHA, ObjSize: objSize, Rep: rep, Descs: map[[32]byte]*Descriptor{}}
	for id := range reach.Role {
		if d := sh.Src.desc(id); d != nil {
			r.Descs[id] = d
		}
	}
	return sh.Meta.Publish(r, hook)
}

// Adopt loads every valid published root's descriptors into the Source and marks
// their chunks known (what a restarted shadow layer does from persisted state).
func (sh *Shadow) Adopt() (roots []*Root, bad map[string]error) {
	roots, _, bad = sh.Meta.Load()
	for _, r := range roots {
		sh.Src.AddDescriptors(r.Descs)
	}
	return
}

// Live is the physical dependency set of a set of shadow roots: the logical
// objects each reaches and the production chunks those need.
type Live struct {
	Objects map[[32]byte]repz3.DepRole
	Chunks  map[[32]byte]bool
}

// LiveSet walks each root with the contract's own reachability over the
// production-backed Source (rep objects and sections are read from the CAS).
func (sh *Shadow) LiveSet(roots []*Root) (*Live, error) {
	l := &Live{Objects: map[[32]byte]repz3.DepRole{}, Chunks: map[[32]byte]bool{}}
	for _, r := range roots {
		sh.Src.AddDescriptors(r.Descs)
		reach, err := sh.Env().Reachable(r.Rep)
		if err != nil {
			return nil, fmt.Errorf("root %q: %w", r.Key, err)
		}
		for id, role := range reach.Role {
			l.Objects[id] = role
			for _, c := range sh.Src.Chunks(id) {
				l.Chunks[c] = true
			}
		}
	}
	if missing := sh.CheckPresent(l.Chunks); len(missing) > 0 {
		return l, fmt.Errorf("%w: %d live shadow chunks absent from the CAS (first %x)", repz3.ErrMissing, len(missing), missing[0][:6])
	}
	return l, nil
}

// CheckPresent stats every chunk of the set (the shadow analogue of the
// production live-set rule that every referenced chunk must exist) and returns
// the missing ones.
func (sh *Shadow) CheckPresent(chunks map[[32]byte]bool) (missing [][32]byte) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for c := range chunks {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := sh.Br.Stat(c); err != nil {
				mu.Lock()
				missing = append(missing, c)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	sortSHAFast(missing)
	return missing
}

// WriteLive writes the chunk set in the format the bridge's Z3_SHADOW_LIVE reads.
func WriteLive(path string, chunks map[[32]byte]bool) error {
	hexes := make([]string, 0, len(chunks))
	for c := range chunks {
		hexes = append(hexes, hex.EncodeToString(c[:]))
	}
	sort.Strings(hexes)
	return os.WriteFile(path, []byte(strings.Join(hexes, "\n")+"\n"), 0o644)
}

// ConcatBundle makes a KindConcat representation whose STORED members are the
// given byte strings (each one logical component, any size): the vehicle for
// exercising the component mapping on exact sizes.
func ConcatBundle(parts ...[]byte) (*kinds.Bundle, error) {
	rep := &repz3.Rep{Kind: repz3.KindConcat}
	for _, p := range parts {
		rep.Members = append(rep.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(p), Size: int64(len(p))})
		rep.Size += int64(len(p))
	}
	raw, err := rep.Marshal()
	if err != nil {
		return nil, err
	}
	return &kinds.Bundle{Rep: rep, Raw: raw, ID: sha256.Sum256(raw), Objects: parts}, nil
}
