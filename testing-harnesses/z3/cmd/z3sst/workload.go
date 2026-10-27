package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// The Z3-30 workload is generated once, here, as deterministic operation logs plus a
// plan; the producer drivers (oracle/sst/*.py, oracle/sst/pebblegen) only replay them
// against the real engines. No producer is involved in deciding what the data is.

type rng struct{ s uint64 }

func (r *rng) next() uint64 {
	r.s += 0x9e3779b97f4a7c15
	z := r.s
	z = (z ^ z>>30) * 0xbf58476d1ce4e5b9
	z = (z ^ z>>27) * 0x94d049bb133111eb
	return z ^ z>>31
}
func (r *rng) n(m int) int { return int(r.next() % uint64(m)) }
func (r *rng) bytes(n int) []byte {
	b := make([]byte, n)
	for i := 0; i < n; {
		v := r.next()
		for k := 0; k < 8 && i < n; k++ {
			b[i] = byte(v >> (8 * k))
			i++
		}
	}
	return b
}

var words = []string{"alpha", "bravo", "delta", "ember", "falcon", "garnet", "harbor", "indigo", "jasper", "kestrel",
	"lumen", "meadow", "nectar", "onyx", "pillar", "quartz", "raven", "sable", "tundra", "umber", "vertex", "willow", "xenon", "yonder", "zephyr"}

// val is the value of (class, id) at version ver: deterministic in those three only.
func val(class byte, id, ver int, seed uint64) []byte {
	r := &rng{seed ^ uint64(class)<<56 ^ uint64(id)*0x100000001b3 ^ uint64(ver)<<40}
	switch class {
	case 'm': // small metadata, 24-72 B
		s := fmt.Sprintf("id=%d;v=%d;n=%d;f=%x;t=%d;", id, ver, r.n(1<<20), r.next()&0xffff, 1700000000+r.n(1<<24))
		for len(s) < 24+r.n(49) {
			s += fmt.Sprintf("%x", r.next()&0xfff)
		}
		return []byte(s)
	case 'j', 'z': // JSON text, 150-700 B
		s := fmt.Sprintf(`{"id":%d,"ver":%d,"name":"%s-%s","score":%.3f,"tags":[`, id, ver, words[r.n(len(words))], words[r.n(len(words))], float64(r.n(100000))/997)
		for t := 0; t < 2+r.n(5); t++ {
			if t > 0 {
				s += ","
			}
			s += `"` + words[r.n(len(words))] + `"`
		}
		s += `],"note":"`
		for len(s) < 150+r.n(550) {
			s += words[r.n(len(words))] + " "
		}
		return []byte(s + `"}`)
	case 'b': // medium binary 600-3000 B: half random, half a repeating structure
		n := 600 + r.n(2400)
		b := r.bytes(n / 2)
		pat := r.bytes(16)
		for len(b) < n {
			b = append(b, pat...)
		}
		return b[:n]
	case 'l': // larger blob, 8-40 KiB, incompressible
		return r.bytes(8<<10 + r.n(32<<10))
	case 'r': // repeated: a dozen distinct values shared by many keys (independent of id and ver)
		t := &rng{seed ^ 0x5eed ^ uint64(id%12)*7919}
		return t.bytes(300 + t.n(300))
	}
	panic("class")
}

func key(prefix string, class byte, id int) []byte {
	return []byte(fmt.Sprintf("%s%c/%09d", prefix, class, id))
}

type kv struct{ k, v []byte }

type entry struct {
	class   byte
	id, ver int
	prefix  string
}

func (e entry) key() []byte { return key(e.prefix, e.class, e.id) }

type model struct {
	seed uint64
	m    map[string]entry
}

func (md *model) put(e entry) { md.m[string(e.key())] = e }
func (md *model) sorted() []entry {
	ks := make([]string, 0, len(md.m))
	for k := range md.m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	out := make([]entry, len(ks))
	for i, k := range ks {
		out[i] = md.m[k]
	}
	return out
}
func (md *model) logical() (n int, bytes int64) {
	for _, e := range md.m {
		n++
		bytes += int64(len(e.key()) + len(val(e.class, e.id, e.ver, md.seed)))
	}
	return
}

type opWriter struct {
	f *os.File
	w *bufio.Writer
	n int
}

func newOps(dir, name string) *opWriter {
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		panic(err)
	}
	return &opWriter{f: f, w: bufio.NewWriterSize(f, 1<<20)}
}
func (o *opWriter) put(k, v []byte) {
	o.w.WriteByte(1)
	o.w.Write(binary.AppendUvarint(nil, uint64(len(k))))
	o.w.Write(k)
	o.w.Write(binary.AppendUvarint(nil, uint64(len(v))))
	o.w.Write(v)
	o.n++
}
func (o *opWriter) del(k []byte) {
	o.w.WriteByte(2)
	o.w.Write(binary.AppendUvarint(nil, uint64(len(k))))
	o.w.Write(k)
	o.n++
}
func (o *opWriter) close() { o.w.Flush(); o.f.Close() }

type step map[string]any
type rev struct {
	Rev, Mutation, Class string
	Entries              int
	Logical              int64
	Base                 string
}
type family struct {
	Name  string `json:"name"`
	Steps []step `json:"steps"`
	Revs  []rev  `json:"revs"`
}

const (
	nM, nJ, nB, nL, nR = 45000, 30000, 12000, 250, 12000
)

// cmdGenOps writes the operation logs and plan.json into a directory.
func cmdGenOps(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gen-ops <dir>")
	}
	dir := args[0]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	const seed = 30
	base := &model{seed: seed, m: map[string]entry{}}
	add := func(md *model, class byte, n int, step, off int, prefix string) {
		for i := 0; i < n; i++ {
			md.put(entry{class: class, id: i*step + off, prefix: prefix})
		}
	}
	add(base, 'm', nM, 2, 0, "")
	add(base, 'j', nJ, 2, 0, "")
	add(base, 'b', nB, 2, 0, "")
	add(base, 'l', nL, 2, 0, "")
	add(base, 'r', nR, 2, 0, "")
	copyModel := func(md *model) *model {
		c := &model{seed: md.seed, m: make(map[string]entry, len(md.m))}
		for k, v := range md.m {
			c.m[k] = v
		}
		return c
	}
	writeAll := func(name string, es []entry, md *model) {
		o := newOps(dir, name)
		for _, e := range es {
			o.put(e.key(), val(e.class, e.id, e.ver, md.seed))
		}
		o.close()
	}
	shuffled := func(md *model, tag uint64) []entry {
		es := md.sorted()
		r := &rng{seed ^ tag}
		for i := len(es) - 1; i > 0; i-- {
			j := r.n(i + 1)
			es[i], es[j] = es[j], es[i]
		}
		return es
	}
	pick := func(md *model, frac float64, tag uint64, skipBig bool) []entry {
		es := md.sorted()
		r := &rng{seed ^ tag}
		var out []entry
		for _, e := range es {
			if skipBig && (e.class == 'l') {
				continue
			}
			if float64(r.n(1_000_000))/1e6 < frac {
				out = append(out, e)
			}
		}
		return out
	}
	writeAll("base.ops", shuffled(base, 1), base)
	writeAll("reload.ops", base.sorted(), base)

	// chain: cumulative state
	chain := copyModel(base)
	// append: 2000 keys sorting after every existing key
	{
		o := newOps(dir, "append.ops")
		for i := 0; i < 2000; i++ {
			e := entry{class: 'z', id: i}
			chain.put(e)
			o.put(e.key(), val('z', i, 0, seed))
		}
		o.close()
	}
	// upd: 1% overwritten (new version)
	updSet := pick(chain, 0.01, 2, false)
	{
		o := newOps(dir, "upd.ops")
		for _, e := range updSet {
			e.ver++
			chain.put(e)
			o.put(e.key(), val(e.class, e.id, e.ver, seed))
		}
		o.close()
	}
	// ins: 2% new keys interleaved (odd ids) in m, j, b
	{
		o := newOps(dir, "ins.ops")
		r := &rng{seed ^ 3}
		cnt := 0
		for _, c := range []struct {
			class byte
			n     int
		}{{'m', nM}, {'j', nJ}, {'b', nB}} {
			for i := 0; i < c.n; i++ {
				if r.n(100) < 2 {
					e := entry{class: c.class, id: i*2 + 1}
					chain.put(e)
					o.put(e.key(), val(e.class, e.id, 0, seed))
					cnt++
				}
			}
		}
		o.close()
	}
	// del: 2% of existing keys (not the large blobs)
	delSet := pick(chain, 0.02, 4, true)
	{
		o := newOps(dir, "del.ops")
		for _, e := range delSet {
			delete(chain.m, string(e.key()))
			o.del(e.key())
		}
		o.close()
	}
	updSet2 := pick(chain, 0.01, 5, false)
	{
		o := newOps(dir, "upd2.ops")
		for _, e := range updSet2 {
			e.ver++
			chain.put(e)
			o.put(e.key(), val(e.class, e.id, e.ver, seed))
		}
		o.close()
	}

	// variants derive from r0 (base) independently
	vmod := func(f func(md *model, o *opWriter), name string) *model {
		md := copyModel(base)
		o := newOps(dir, name)
		f(md, o)
		o.close()
		return md
	}
	mApp := vmod(func(md *model, o *opWriter) {
		for i := 0; i < 2000; i++ {
			e := entry{class: 'z', id: i}
			md.put(e)
			o.put(e.key(), val('z', i, 0, seed))
		}
	}, "v-append.ops")
	mUpd := vmod(func(md *model, o *opWriter) {
		for _, e := range pick(base, 0.01, 12, false) {
			e.ver++
			md.put(e)
			o.put(e.key(), val(e.class, e.id, e.ver, seed))
		}
	}, "v-upd.ops")
	mIns := vmod(func(md *model, o *opWriter) {
		r := &rng{seed ^ 13}
		for _, c := range []struct {
			class byte
			n     int
		}{{'m', nM}, {'j', nJ}, {'b', nB}} {
			for i := 0; i < c.n; i++ {
				if r.n(100) < 2 {
					e := entry{class: c.class, id: i*2 + 1}
					md.put(e)
					o.put(e.key(), val(e.class, e.id, 0, seed))
				}
			}
		}
	}, "v-ins.ops")
	mDel := vmod(func(md *model, o *opWriter) {
		for _, e := range pick(base, 0.02, 14, true) {
			delete(md.m, string(e.key()))
			o.del(e.key())
		}
	}, "v-del.ops")
	mChurn := vmod(func(md *model, o *opWriter) {
		for _, e := range pick(base, 0.45, 16, false) {
			e.ver++
			md.put(e)
			o.put(e.key(), val(e.class, e.id, e.ver, seed))
		}
	}, "v-churn.ops")
	mWhole := vmod(func(md *model, o *opWriter) {
		for _, e := range base.sorted() {
			e.ver = 100
			md.put(e)
			o.put(e.key(), val(e.class, e.id, e.ver, seed))
		}
	}, "v-wholesale.ops")
	mRand := vmod(func(md *model, o *opWriter) {
		r := &rng{seed ^ 15}
		for _, e := range base.sorted() {
			o.put(e.key(), r.bytes(len(val(e.class, e.id, 0, seed))))
		}
	}, "v-randvals.ops")
	// unrelated: a different key space and different values
	unrel := &model{seed: seed + 1000, m: map[string]entry{}}
	for _, c := range []struct {
		class byte
		n     int
	}{{'m', nM}, {'j', nJ}, {'b', nB}, {'l', nL}, {'r', nR}} {
		add(unrel, c.class, c.n, 3, 1, "q")
	}
	writeAll("unrelated.ops", shuffled(unrel, 6), unrel)
	// rekey: same values, every key renamed
	{
		o := newOps(dir, "rekey.ops")
		for _, e := range shuffled(base, 7) {
			o.put(append([]byte("x/"), e.key()...), val(e.class, e.id, e.ver, seed))
		}
		o.close()
	}

	nb, lb := base.logical()
	mk := func(md *model, name, mut, class, b string) rev {
		n, l := md.logical()
		return rev{name, mut, class, n, l, b}
	}
	var fams []family
	loadBase := []step{{"c": "newdb"}, {"c": "apply", "ops": "base.ops", "batch": 1000}, {"c": "compact"}}
	// chain
	chainSteps := append(append([]step{}, loadBase...), step{"c": "snapshot", "rev": "c0", "dir": true})
	chainSteps = append(chainSteps,
		step{"c": "apply", "ops": "append.ops", "batch": 500}, step{"c": "flush"}, step{"c": "snapshot", "rev": "c1", "dir": true},
		step{"c": "apply", "ops": "upd.ops", "batch": 500}, step{"c": "flush"}, step{"c": "snapshot", "rev": "c2", "dir": true},
		step{"c": "apply", "ops": "ins.ops", "batch": 500}, step{"c": "compact"}, step{"c": "snapshot", "rev": "c3", "dir": true},
		step{"c": "apply", "ops": "del.ops", "batch": 500}, step{"c": "compact"}, step{"c": "snapshot", "rev": "c4", "dir": true},
		step{"c": "apply", "ops": "upd2.ops", "batch": 500}, step{"c": "compact"}, step{"c": "snapshot", "rev": "c5", "dir": true},
		step{"c": "reopen", "scale": 0.5}, step{"c": "compact"}, step{"c": "snapshot", "rev": "c6", "dir": true},
		step{"c": "reopen", "scale": 2.0}, step{"c": "compact"}, step{"c": "snapshot", "rev": "c7", "dir": true})
	// chain logical states: c0..c2 additive; replay in order for the metadata
	cm := copyModel(base)
	var crevs []rev
	crevs = append(crevs, mk(cm, "c0", "base load (random key order, batches of 1000) + full compaction", "PRODUCER_WORKLOAD", ""))
	// the chain model is replayed from the op files' semantics above
	cm2 := copyModel(base)
	for i := 0; i < 2000; i++ {
		cm2.put(entry{class: 'z', id: i})
	}
	crevs = append(crevs, mk(cm2, "c1", "append 2000 sorted keys, flush only", "PRODUCER_WORKLOAD", "c0"))
	for _, e := range updSet {
		e.ver++
		cm2.put(e)
	}
	crevs = append(crevs, mk(cm2, "c2", "overwrite 1% of keys, flush only (new L0 table)", "PRODUCER_WORKLOAD", "c1"))
	// c3..c7 states equal the final chain model after the respective ops; recompute stepwise
	r3 := &rng{seed ^ 3}
	for _, c := range []struct {
		class byte
		n     int
	}{{'m', nM}, {'j', nJ}, {'b', nB}} {
		for i := 0; i < c.n; i++ {
			if r3.n(100) < 2 {
				cm2.put(entry{class: c.class, id: i*2 + 1})
			}
		}
	}
	crevs = append(crevs, mk(cm2, "c3", "insert 2% keys in the middle + full compaction", "PRODUCER_WORKLOAD", "c2"))
	for _, e := range delSet {
		delete(cm2.m, string(e.key()))
	}
	crevs = append(crevs, mk(cm2, "c4", "delete 2% keys + full compaction", "PRODUCER_WORKLOAD", "c3"))
	for _, e := range updSet2 {
		e.ver++
		cm2.put(e)
	}
	crevs = append(crevs, mk(cm2, "c5", "overwrite 1% keys + full compaction", "PRODUCER_WORKLOAD", "c4"))
	crevs = append(crevs, mk(cm2, "c6", "same logical state recompacted into half-size tables (split)", "PRODUCER_WORKLOAD", "c5"))
	crevs = append(crevs, mk(cm2, "c7", "same logical state recompacted into double-size tables (merge)", "PRODUCER_WORKLOAD", "c6"))
	fams = append(fams, family{"chain", chainSteps, crevs})

	// variants
	vsteps := append(append([]step{}, loadBase...), step{"c": "savebase"}, step{"c": "snapshot", "rev": "r0"})
	vrevs := []rev{mk(base, "r0", "base load + full compaction", "PRODUCER_WORKLOAD", "")}
	variant := func(name, mut, class string, md *model, steps ...step) {
		vsteps = append(vsteps, step{"c": "frombase"})
		vsteps = append(vsteps, steps...)
		vsteps = append(vsteps, step{"c": "snapshot", "rev": name})
		vrevs = append(vrevs, mk(md, name, mut, class, "r0"))
	}
	variant("v01-append-flush", "append 2000 sorted keys, flush only", "PRODUCER_WORKLOAD", mApp, step{"c": "apply", "ops": "v-append.ops", "batch": 500}, step{"c": "flush"})
	variant("v02-update-flush", "overwrite 1% of keys, flush only", "PRODUCER_WORKLOAD", mUpd, step{"c": "apply", "ops": "v-upd.ops", "batch": 500}, step{"c": "flush"})
	variant("v03-update-compact", "overwrite 1% of keys + full compaction", "PRODUCER_WORKLOAD", mUpd, step{"c": "apply", "ops": "v-upd.ops", "batch": 500}, step{"c": "compact"})
	variant("v04-insert-compact", "insert 2% keys in the middle + full compaction", "PRODUCER_WORKLOAD", mIns, step{"c": "apply", "ops": "v-ins.ops", "batch": 500}, step{"c": "compact"})
	variant("v10-churn", "overwrite 45% of keys; the engine compacts on its own (LevelDB) or fully (RocksDB, Pebble)", "PRODUCER_WORKLOAD", mChurn, step{"c": "apply", "ops": "v-churn.ops", "batch": 500}, step{"c": "autocompact"})
	variant("v05-delete-compact", "delete 2% keys + full compaction", "PRODUCER_WORKLOAD", mDel, step{"c": "apply", "ops": "v-del.ops", "batch": 500}, step{"c": "compact"})
	variant("v06-recompact", "full compaction of an unchanged database", "PRODUCER_WORKLOAD", base, step{"c": "compact"})
	for _, x := range []struct {
		name, mut string
		scale     float64
	}{{"v09-reexport", "same logical set rebuilt in sorted order, batches of 5000 (different sequence numbers and table composition)", 1},
		{"x01-reexport-half", "identical to v09 but half-size tables (tables split; same sequence numbers as v09)", 0.5},
		{"x02-reexport-double", "identical to v09 but double-size tables (tables merged; same sequence numbers as v09)", 2}} {
		vsteps = append(vsteps, step{"c": "newdb", "scale": x.scale}, step{"c": "apply", "ops": "reload.ops", "batch": 5000}, step{"c": "compact"}, step{"c": "snapshot", "rev": x.name})
		vrevs = append(vrevs, mk(base, x.name, x.mut, "REAL_REEXPORT", ""))
	}
	variant("n01-wholesale", "every value rewritten (same keys) + full compaction", "PRODUCER_WORKLOAD", mWhole, step{"c": "apply", "ops": "v-wholesale.ops", "batch": 1000}, step{"c": "compact"})
	variant("n02-randvals", "every value replaced by random bytes + full compaction", "PRODUCER_WORKLOAD", mRand, step{"c": "apply", "ops": "v-randvals.ops", "batch": 1000}, step{"c": "compact"})
	vsteps = append(vsteps, step{"c": "newdb"}, step{"c": "apply", "ops": "unrelated.ops", "batch": 1000}, step{"c": "compact"}, step{"c": "snapshot", "rev": "n03-unrelated"})
	vrevs = append(vrevs, mk(unrel, "n03-unrelated", "unrelated key space and values (different seed)", "PRODUCER_WORKLOAD", ""))
	vsteps = append(vsteps, step{"c": "newdb"}, step{"c": "apply", "ops": "rekey.ops", "batch": 1000}, step{"c": "compact"}, step{"c": "snapshot", "rev": "n04-rekey"})
	rk := rev{"n04-rekey", "identical values under renamed keys (key space changed)", "PRODUCER_WORKLOAD", nb, lb + 2*int64(nb), ""}
	vrevs = append(vrevs, rk)
	fams = append(fams, family{"variants", vsteps, vrevs})

	// codecs: the base load under other block compressions (characterisation and negative control:
	// only none and Snappy are expressed; a producer skips the codecs it does not have)
	var csteps []step
	var crevs2 []rev
	for _, c := range []string{"none", "lz4", "zlib", "zstd"} {
		csteps = append(csteps, step{"c": "newdb", "codec": c}, step{"c": "apply", "ops": "base.ops", "batch": 1000}, step{"c": "compact"}, step{"c": "snapshot", "rev": "k-" + c})
		crevs2 = append(crevs2, mk(base, "k-"+c, "base load with block compression "+c, "PRODUCER_WORKLOAD", ""))
	}
	fams = append(fams, family{"codecs", csteps, crevs2})

	pb, _ := json.MarshalIndent(map[string]any{"seed": seed, "base_entries": nb, "base_logical": lb, "families": fams}, "", " ")
	return os.WriteFile(filepath.Join(dir, "plan.json"), pb, 0o644)
}
