package kinds

import (
	"archive/tar"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"zeros3-z3/carz3"
	"zeros3-z3/gitz3"
	"zeros3-z3/merklez3"
	"zeros3-z3/repz3"
)

// Stage is one object state in a chain; each stage is stored on top of the earlier
// ones (the cost of a state is what it adds to a store holding its predecessors).
type Stage struct {
	Label string
	Data  []byte
}

// Chain is one migrated workload: stages plus the adapter that analyses a stage
// against the current store.
type Chain struct {
	Name   string
	Stages []Stage
	Encode func(st *repz3.MemStore, data []byte) (*Bundle, error)
	// NegativeOK marks stages that are expected to lose to CDC (the analyzer must fall back).
	Negative map[string]bool
}

func moduleRoot() string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(f), "..", "..")
}

type xorshift uint64

func (x *xorshift) next() uint64 {
	*x ^= *x << 13
	*x ^= *x >> 7
	*x ^= *x << 17
	return uint64(*x)
}

func (x *xorshift) fill(b []byte) {
	for i := 0; i < len(b); i += 8 {
		v := x.next()
		for j := 0; j < 8 && i+j < len(b); j++ {
			b[i+j] = byte(v >> (8 * uint(j)))
		}
	}
}

const sqlitePS = 4096

// synthSQLite builds a database-shaped file: page 1 carries the real header
// fields, 80 % of the other pages are b-tree pages (type byte 0x0d), 20 % are
// overflow-style pages whose first four bytes are the next page number.
// sqlitez3 is byte-level, so no real b-tree is needed to exercise it.
func synthSQLite(seed uint64, pages int) [][]byte {
	rng := xorshift(seed | 1)
	out := make([][]byte, pages)
	for i := range out {
		p := make([]byte, sqlitePS)
		rng.fill(p)
		switch {
		case i == 0:
			copy(p, "SQLite format 3\x00")
			p[16], p[17] = 0x10, 0x00 // page size 4096
		case rng.next()%5 == 0:
			p[0], p[1], p[2], p[3] = 0, 0, 0, 0 // overflow chain: head set by joinSQLite
		default:
			p[0] = 0x0d
		}
		out[i] = p
	}
	return out
}

// joinSQLite concatenates pages and writes the overflow heads as the next page
// number, so a sequential chain has the same delta after every renumbering.
func joinSQLite(pages [][]byte) []byte {
	var b bytes.Buffer
	for i, p := range pages {
		q := append([]byte(nil), p...)
		if i > 0 && q[0] == 0 {
			n := uint32(i + 2)
			q[0], q[1], q[2], q[3] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
		}
		b.Write(q)
	}
	return b.Bytes()
}

func sparseUpdate(pages [][]byte, seed uint64, n int) [][]byte {
	out := append([][]byte(nil), pages...)
	rng := xorshift(seed | 1)
	for k := 0; k < n; k++ {
		i := 1 + int(rng.next()%uint64(len(out)-1))
		q := append([]byte(nil), out[i]...)
		off := 8 + int(rng.next()%uint64(len(q)-300))
		rng.fill(q[off : off+200])
		out[i] = q
	}
	return out
}

// SQLiteChain is the Z3-18 state set at laboratory scale: first sight, sparse
// updates, a VACUUM-like rewrite (a tenth of the pages dropped, everything else
// moved and overflow heads renumbered) and one wholesale rewrite (a negative).
func SQLiteChain(pages int) *Chain {
	r0 := synthSQLite(18, pages)
	sparse := sparseUpdate(r0, 5, 40)
	var vac [][]byte
	rng := xorshift(77)
	for i, p := range sparse {
		if i > 0 && rng.next()%10 == 0 {
			continue
		}
		vac = append(vac, p)
	}
	whole := synthSQLite(99, pages)
	return &Chain{Name: "sqlite", Negative: map[string]bool{"wholesale": true},
		Stages: []Stage{{"first sight", joinSQLite(r0)}, {"sparse updates", joinSQLite(sparse)}, {"vacuum", joinSQLite(vac)}, {"wholesale", joinSQLite(whole)}},
		Encode: func(_ *repz3.MemStore, d []byte) (*Bundle, error) {
			b, _, err := EncodeSQLite(bytes.NewReader(d), int64(len(d)))
			return b, err
		}}
}

// CARChains are targeted Z3-14 families: reorder, add/remove-style growth and a
// CARv2 wrapper/index change.
func CARChains(sc carz3.Scale) []*Chain {
	want := map[string]int{"v1-reorder": 2, "v1-add": 3, "v2-wrap": 3}
	var out []*Chain
	for _, f := range carz3.Families(sc) {
		n, ok := want[strings.TrimPrefix(f.Name, sc.Name+"-")]
		if !ok {
			continue
		}
		c := &Chain{Name: "car/" + f.Name, Encode: func(_ *repz3.MemStore, d []byte) (*Bundle, error) {
			b, _, err := EncodeCAR(bytes.NewReader(d), int64(len(d)))
			return b, err
		}}
		for _, r := range f.Revs[:n] {
			c.Stages = append(c.Stages, Stage{r.Label, r.Data})
		}
		out = append(out, c)
	}
	return out
}

// GitFixtureChain is the committed SHA-1 OFS pack and the REF_DELTA repack of the
// same objects.
func GitFixtureChain(algo gitz3.Algo) (*Chain, error) {
	var st []Stage
	for _, n := range []string{"ofs", "ref"} {
		b, err := os.ReadFile(filepath.Join(moduleRoot(), "gitz3", "testdata", fmt.Sprintf("%s-%s.pack", algo, n)))
		if err != nil {
			return nil, err
		}
		st = append(st, Stage{n, b})
	}
	return gitChain("git/fixture-"+algo.String(), algo, st), nil
}

func gitChain(name string, algo gitz3.Algo, st []Stage) *Chain {
	return &Chain{Name: name, Stages: st, Encode: func(_ *repz3.MemStore, d []byte) (*Bundle, error) {
		b, _, err := EncodeGit(d, algo)
		return b, err
	}}
}

// GitRepackChain is the synthetic repack scenario produced by the installed git
// (the optional Z3-16 producer): the same 31-commit history repacked three ways,
// then one commit later. ok is false when git is not installed.
func GitRepackChain(work string, scale int) (c *Chain, ok bool, err error) {
	if _, e := exec.LookPath(gitz3.GitBin()); e != nil {
		return nil, false, nil
	}
	h := gitz3.LinearHistory(7, scale, 31)
	h31 := h.Prefix(31)
	var st []Stage
	for _, s := range []struct {
		label string
		h     *gitz3.History
		spec  gitz3.RepackSpec
	}{
		{"-f repack", h31, gitz3.SpecDefault},
		{"OFS to REF", h31, gitz3.SpecRefDelta},
		{"reversed order", h31, gitz3.SpecReverse},
		{"+1 commit, -f", h, gitz3.SpecDefault},
	} {
		r, err := gitz3.Build(work, "rp-"+s.label[:2]+fmt.Sprint(len(st)), gitz3.SHA1, s.h, s.spec)
		if err != nil {
			return nil, true, err
		}
		pp, err := r.PackPairs()
		if err != nil || len(pp) != 1 {
			return nil, true, fmt.Errorf("repack %q: %d packs (%v)", s.label, len(pp), err)
		}
		st = append(st, Stage{s.label, pp[0].Pack})
	}
	return gitChain("git/repack", gitz3.SHA1, st), true, nil
}

// MerkleChain is a 20 000-key trie snapshot, then one scattered key updated (the
// known-subtree case), then ten.
func MerkleChain(h merklez3.HashFn, keys int) (*Chain, error) {
	cfg := merklez3.Config{Hash: h, LeafMax: 8}
	s0 := merklez3.NewState(15, keys)
	states := []struct {
		label string
		s     *merklez3.State
	}{{"first sight", s0}, {"1 key updated", s0.Update(s0.PickScattered(1, 1))}}
	states = append(states, struct {
		label string
		s     *merklez3.State
	}{"+10 keys updated", states[1].s.Update(states[1].s.PickScattered(2, 10))})
	c := &Chain{Name: "merkle/" + h.Name, Encode: func(st *repz3.MemStore, d []byte) (*Bundle, error) {
		b, _, err := EncodeMerkle(d, h, st)
		return b, err
	}}
	for _, x := range states {
		ms := merklez3.NewMapStore()
		root, _ := x.s.BuildInto(cfg, ms)
		var buf bytes.Buffer
		if _, err := merklez3.Snapshot(ms, root, &buf); err != nil {
			return nil, err
		}
		c.Stages = append(c.Stages, Stage{x.label, buf.Bytes()})
	}
	return c, nil
}

// TarLayer writes an uncompressed tar of named files with fixed metadata.
func TarLayer(names []string, files map[string][]byte) []byte {
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, n := range names {
		w.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), ModTime: epoch, Format: tar.FormatUSTAR})
		w.Write(files[n])
	}
	w.Close()
	return b.Bytes()
}

// CompoundChain is an OCI-like tar layer holding a config file, a library, a SQLite
// database and a CAR export (the DERIVED members): first sight, then the database
// with sparse updates.
func CompoundChain(pages int) *Chain {
	r0 := synthSQLite(20, pages)
	db0, db1 := joinSQLite(r0), joinSQLite(sparseUpdate(r0, 9, 30))
	rng := xorshift(4)
	lib := make([]byte, 512<<10)
	rng.fill(lib)
	car := carz3.Families(carz3.TinyBlocks)[0].Revs[0].Data // 87 KiB, not a multiple of 512: the tar pads it
	names := []string{"etc/app.conf", "lib/libapp.so", "data/app.db", "data/blocks.car"}
	mk := func(db []byte) []byte {
		return TarLayer(names, map[string][]byte{"etc/app.conf": []byte("mode=prod\nretries=3\n"), "lib/libapp.so": lib, "data/app.db": db, "data/blocks.car": car})
	}
	return &Chain{Name: "compound/tar+sqlite", Stages: []Stage{{"first sight", mk(db0)}, {"db sparse updates", mk(db1)}},
		Encode: func(_ *repz3.MemStore, d []byte) (*Bundle, error) {
			b, _, err := EncodeTarLayer(d, 64<<10)
			return b, err
		}}
}

var epoch = time.Unix(1700000000, 0).UTC()

// Scale sizes the chains: the tests use Small, the report Full.
type Scale struct {
	CAR           carz3.Scale
	SQLitePages   int
	CompoundPages int
	MerkleKeys    int
	GitScale      int
}

var (
	Small = Scale{carz3.TinyBlocks, 600, 300, 3000, 2}
	Full  = Scale{carz3.BigBlocks, 3000, 1000, 20000, 6}
)

// Chains returns every migrated workload (the git repack chain only when git is
// installed).
func Chains(sc Scale, work string) ([]*Chain, error) {
	out := CARChains(sc.CAR)
	out = append(out, SQLiteChain(sc.SQLitePages), CompoundChain(sc.CompoundPages))
	m, err := MerkleChain(merklez3.HashSHA3_256, sc.MerkleKeys)
	if err != nil {
		return nil, err
	}
	out = append(out, m)
	for _, a := range []gitz3.Algo{gitz3.SHA1, gitz3.SHA256} {
		g, err := GitFixtureChain(a)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if g, ok, err := GitRepackChain(work, sc.GitScale); err != nil {
		return nil, err
	} else if ok {
		out = append(out, g)
	}
	return out, nil
}
