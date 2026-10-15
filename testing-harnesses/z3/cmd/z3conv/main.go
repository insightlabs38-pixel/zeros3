// Command z3conv prints the Z3-21 measurements of the converged representation
// contract: the reachability policy study (Expand vs dependency sidecar vs the
// contract as built), native-index ownership and accelerator rebuild, the exact
// gzip outer and its compositions, Git point reads, the validation matrix and the
// per-object overhead sensitivity.
//
//	z3conv [-scale small|full] [-only matrix,policy,native,gzip,git,sens]
//
// Everything is derived from bytes the contract persists; the Git repack chain
// needs the optional git producer and is skipped without it.
package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"zeros3-z3/gitz3"
	"zeros3-z3/merklez3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

type run struct {
	ch  *kinds.Chain
	st  *repz3.MemStore
	cat *repz3.Catalog
	ids []repz3.SHA
	// per stage
	cost, cdc []repz3.Cost
	exact     []bool
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "z3conv:", err)
		os.Exit(1)
	}
}

func kib(n int64) string {
	switch {
	case n >= 10<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 10<<10:
		return fmt.Sprintf("%dK", n>>10)
	}
	return fmt.Sprintf("%d", n)
}

func execute(ch *kinds.Chain) *run {
	r := &run{ch: ch, st: repz3.NewMemStore()}
	r.cat = kinds.NewCatalog(r.st)
	cst := repz3.NewMemStore()
	for _, s := range ch.Stages {
		cand, err := ch.Encode(r.st, r.cat, s.Data)
		must(err)
		id, c, err := kinds.Commit(r.st, r.cat, cand)
		must(err)
		var out bytes.Buffer
		n, sum, err := kinds.NewEnv(r.st).Reconstruct(id, &out)
		r.exact = append(r.exact, err == nil && n == int64(len(s.Data)) && sum == sha256.Sum256(s.Data) && bytes.Equal(out.Bytes(), s.Data))
		cdc, err := kinds.CDC(s.Data)
		must(err)
		_, dc, err := kinds.Commit(cst, nil, cdc)
		must(err)
		r.ids, r.cost, r.cdc = append(r.ids, id), append(r.cost, c), append(r.cdc, dc)
	}
	return r
}

func main() {
	scale := flag.String("scale", "full", "small|full")
	only := flag.String("only", "matrix,policy,native,gzip,git,sens", "sections")
	flag.Parse()
	sc := kinds.Full
	if *scale == "small" {
		sc = kinds.Small
	}
	work, _ := os.MkdirTemp("", "z3conv")
	defer os.RemoveAll(work)
	chains, err := kinds.Chains(sc, work)
	must(err)
	long, err := kinds.MerkleLongChain(merklez3.HashSHA3_256, sc.MerkleKeys, 11)
	must(err)
	chains = append(chains, long)
	want := map[string]bool{}
	for _, s := range strings.Split(*only, ",") {
		want[s] = true
	}
	runs := map[string]*run{}
	for _, ch := range chains {
		runs[ch.Name] = execute(ch)
	}
	if want["matrix"] {
		matrix(runs)
	}
	if want["policy"] {
		policy(runs)
	}
	if want["native"] {
		native(sc, runs)
	}
	if want["gzip"] {
		gz(sc, runs)
	}
	if want["git"] {
		git(sc, runs)
	}
	if want["sens"] {
		sens(runs)
	}
}

// rows is the validation matrix of the batch.
var rows = []struct {
	chain  string
	stages []int
}{
	{"car/v1-reorder", []int{0, 1}}, {"car/v1-add", []int{2}},
	{"sqlite", []int{0, 1, 2}},
	{"git/repack", []int{0, 1, 2, 3}},
	{"merkle/sha3-256", []int{0, 1}},
	{"compound/tar+sqlite", []int{0, 1}},
	{"gzip/gzip+tar+derived", []int{0, 1, 2}},
}

func matrix(runs map[string]*run) {
	fmt.Println("== validation matrix: exact reconstruction, accounting by class (bytes actually persisted), reachability vs reconstruction reads")
	fmt.Printf("%-22s %-18s %8s | %8s %7s %7s %6s %5s | %8s | %5s %5s %5s %5s | exact\n",
		"workload", "state", "logical", "payload", "rep", "table", "index", "objs", "cdc", "live", "read", "unread", "bad")
	for _, rw := range rows {
		r := runs[rw.chain]
		if r == nil {
			continue
		}
		for _, i := range rw.stages {
			if i >= len(r.ids) {
				continue
			}
			s, c := r.ch.Stages[i], r.cost[i]
			env := kinds.NewEnv(r.st)
			r.st.ResetReads()
			var out bytes.Buffer
			_, _, err := env.Reconstruct(r.ids[i], &out)
			must(err)
			_, touched := r.st.Touched()
			reach, err := env.Reachable(r.ids[i])
			must(err)
			unread, bad := 0, 0
			for obj, role := range reach.Role {
				if _, ok := touched[obj]; !ok && role != repz3.DepIndex && role != repz3.DepOwned {
					bad++ // live, needed by nothing and not an explained class
				} else if !ok {
					unread++
				}
			}
			for obj := range touched {
				if _, ok := reach.Role[obj]; !ok {
					bad++ // read but not kept alive
				}
			}
			fmt.Printf("%-22s %-18s %8s | %8s %7s %7s %6s %5d | %8s | %5d %5d %5d %5d | %v\n", rw.chain, s.Label, kib(int64(len(s.Data))),
				kib(c.Payload), kib(c.Recipe), kib(c.Table), kib(c.Index), c.Objects, kib(r.cdc[i].Bytes()), len(reach.Role), len(touched), unread, bad, r.exact[i])
		}
	}
	fmt.Println("(unread = live index/owned objects reconstruction does not read; bad = live-but-unexplained plus read-but-not-live, must be 0)")
}

func policy(runs map[string]*run) {
	fmt.Println("\n== reachability policies per root (A Expand, B exact sidecar count|sorted ids, C contract: Expand, owning section where present)")
	fmt.Printf("%-22s %-14s %6s %5s | %9s %5s %9s | %8s %5s %9s %9s | %9s %5s %9s | %6s\n",
		"workload", "state", "via", "reps", "A read", "objs", "A time", "B +store", "+objs", "B read", "B time", "C read", "objs", "C time", "live")
	pick := []struct {
		chain string
		stage []int
	}{{"car/v1-reorder", []int{0, 1}}, {"sqlite", []int{0, 1}}, {"git/repack", []int{0, 3}}, {"merkle/sha3-256", []int{0, 1}}, {"merkle-long/sha3-256", []int{11}}, {"gzip/gzip+tar+derived", []int{0, 1}}}
	for _, p := range pick {
		r := runs[p.chain]
		if r == nil {
			continue
		}
		for _, i := range p.stage {
			s, err := kinds.StudyReach(r.st, r.ids[i])
			must(err)
			fmt.Printf("%-22s %-14s %6d %5d | %9s %5d %9v | %8s %5d %9s %9v | %9s %5d %9v | %6d\n", p.chain, r.ch.Stages[i].Label, s.Via, s.Reps,
				kib(s.A.ReadBytes), s.A.ReadObjs, s.A.Time.Round(10*time.Microsecond),
				kib(s.SidecarB+s.RepDeltaB), s.Sidecars, kib(s.B.ReadBytes), s.B.Time.Round(10*time.Microsecond),
				kib(s.C.ReadBytes), s.C.ReadObjs, s.C.Time.Round(10*time.Microsecond), s.C.Live)
		}
	}
	fmt.Println("(read = bytes of objects other than representation objects; B +store = sidecar components + one section reference per representation)")
}

func native(sc kinds.Scale, runs map[string]*run) {
	fmt.Println("\n== native index ownership: Merkle revision chain (local sections canonical, accelerator derived)")
	fmt.Printf("%-8s %9s %9s %5s %5s %9s | %8s %8s\n", "rev", "idx added", "idx refd", "secs", "objs", "compact", "entries", "records")
	long, err := kinds.MerkleLongChain(merklez3.HashSHA3_256, sc.MerkleKeys, 11)
	must(err)
	st := repz3.NewMemStore()
	cat := kinds.NewCatalog(st)
	var ids []repz3.SHA
	for i, s := range long.Stages {
		b, ms, err := kinds.EncodeMerkle(s.Data, merklez3.HashSHA3_256, st, cat)
		must(err)
		id, c, err := kinds.Commit(st, cat, b)
		must(err)
		ids = append(ids, id)
		var refd int64
		for _, x := range b.Rep.Indexes {
			refd += x.Bytes()
		}
		fmt.Printf("%-8d %9s %9s %5d %5d %9v | %8d %8d\n", i, kib(c.Index), kib(refd), len(b.Rep.Indexes), c.Objects, ms.Compacted, cat.Entries(), cat.Records())
	}
	env := kinds.NewEnv(st)
	live, err := env.ReachableAll(ids)
	must(err)
	fresh := repz3.NewMemStore()
	for obj := range live.Role {
		fresh.Put(st.Bytes(obj))
	}
	pre := cat.Mappings()
	var t0 time.Time
	var re *repz3.Catalog
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	t0 = time.Now()
	re, err = repz3.Rebuild(fresh, kinds.Domain, ids)
	must(err)
	dur := time.Since(t0)
	runtime.GC()
	runtime.ReadMemStats(&after)
	equal := len(pre) == len(re.Mappings())
	if equal {
		rm := re.Mappings()
		for k, v := range pre {
			if w := rm[k]; len(w) != len(v) || (len(v) > 0 && w[0] != v[0]) {
				equal = false
				break
			}
		}
	}
	okAll := true
	for i, id := range ids {
		var out bytes.Buffer
		if _, _, err := kinds.NewEnv(fresh).Reconstruct(id, &out); err != nil || !bytes.Equal(out.Bytes(), long.Stages[i].Data) {
			okAll = false
		}
	}
	fmt.Printf("restart: accelerator %d keys / %d records from %d roots rebuilt in %v, heap %s, equal to the pre-restart accelerator: %v, all %d revisions rebuild from the restart store: %v\n",
		re.Entries(), re.Records(), len(ids), dur.Round(time.Millisecond), kib(int64(after.HeapAlloc)-int64(before.HeapAlloc)), equal, len(ids), okAll)
	// lookup touched bytes: local sections (what a representation does) vs the accelerator
	rep, _ := env.LoadRep(ids[len(ids)-1])
	var key []byte
	errStop := errors.New("stop")
	if err := repz3.ScanIndex(st, rep.Indexes[len(rep.Indexes)-1], func(k []byte, _ repz3.SHA) error { key = append([]byte(nil), k...); return errStop }); !errors.Is(err, errStop) {
		must(err)
	}
	st.ResetReads()
	c, err := env.Open(ids[len(ids)-1])
	must(err)
	_, ok, err := c.Resolve(key)
	must(err)
	_, tb := st.Touched()
	var sum int64
	for obj, n := range tb {
		if obj != ids[len(ids)-1] {
			sum += n
		}
	}
	dom := kinds.Domain(rep, rep.Indexes[0])
	_, found, _ := cat.Resolve(dom, key)
	fmt.Printf("lookup of one node (%d sections): local sections touch %d B (found=%v); the accelerator touches 0 B (found=%v) and names the owning representation(s): %d\n",
		len(rep.Indexes), sum, ok, found, len(cat.Owners(dom, key)))
	// removal: drop the newest root
	cat.Remove(ids[len(ids)-1])
	rem, _ := repz3.Rebuild(fresh, kinds.Domain, ids[:len(ids)-1])
	fmt.Printf("remove newest root: %d -> %d keys; equals a rebuild from the survivors: %v\n", len(pre), cat.Entries(), len(cat.Mappings()) == len(rem.Mappings()))
	// Git and CAR
	if g := runs["git/fixture-sha1"]; g != nil {
		rp, _ := kinds.NewEnv(g.st).LoadRep(g.ids[0])
		gd := kinds.Domain(rp, rp.Indexes[0])
		agree, conflict, n := 0, 0, 0
		must(repz3.ScanIndex(g.st, rp.Indexes[0], func(k []byte, _ repz3.SHA) error {
			n++
			if _, ok, err := g.cat.Resolve(gd, k); ok && err == nil {
				agree++
			} else {
				conflict++
			}
			return nil
		}))
		fmt.Printf("git sha1 (ofs + ref repack of one history, both live): %d ids, %d resolve to one component, %d reported ambiguous; local index %s per pack\n", n, agree, conflict, kib(rp.Indexes[0].Bytes()))
	}
	if r := runs["git/repack"]; r != nil {
		rp, _ := kinds.NewEnv(r.st).LoadRep(r.ids[0])
		fmt.Printf("git repack chain: local index %s (%d records x %d B); accelerator over its %d live packs: %d keys\n", kib(rp.Indexes[0].Bytes()), rp.Indexes[0].Count, rp.Indexes[0].KeyWidth+32, len(r.ids), r.cat.Entries())
	}
	if r := runs["car/v1-reorder"]; r != nil {
		rp, _ := kinds.NewEnv(r.st).LoadRep(r.ids[0])
		fmt.Printf("car (control): %d index sections: the CID derives from the block digest; the accelerator holds %d keys\n", len(rp.Indexes), r.cat.Entries())
	}
	if r := runs["merkle/sha3-256"]; r != nil {
		rp, _ := kinds.NewEnv(r.st).LoadRep(r.ids[0])
		fmt.Printf("merkle first sight: %s of index for %d nodes (64 B each; it is also the VIA list); accelerator over %d roots: %d keys, %d records, %d sections\n",
			kib(r.cost[0].Index), rp.Indexes[0].Count, len(r.ids), r.cat.Entries(), r.cat.Records(), r.cat.NSections())
	}
}

type counting struct{ n int64 }

func (c *counting) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

func peak(f func()) float64 {
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	stop, done := make(chan struct{}), make(chan uint64)
	go func() {
		var hi uint64
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				done <- hi
				return
			default:
				runtime.ReadMemStats(&m)
				hi = max(hi, m.HeapAlloc)
				time.Sleep(time.Millisecond)
			}
		}
	}()
	f()
	close(stop)
	return float64(int64(<-done)-int64(base.HeapAlloc)) / (1 << 20)
}

func gz(sc kinds.Scale, runs map[string]*run) {
	fmt.Println("\n== exact gzip outer: one compressed layer (gzip -> tar -> SQLite + CAR), four representations, bytes persisted per state added to a store holding the earlier ones")
	fmt.Printf("%-18s %-18s %8s | %9s %8s %7s %5s | %9s\n", "variant", "state", "gz bytes", "payload", "table", "rep", "objs", "total")
	for _, v := range kinds.GzipVariants {
		ch := kinds.GzipCompoundChain(v, sc.CompoundPages)
		r := execute(ch)
		for i, s := range ch.Stages {
			c := r.cost[i]
			fmt.Printf("%-18s %-18s %8s | %9s %8s %7s %5d | %9s  exact=%v\n", v, s.Label, kib(int64(len(s.Data))), kib(c.Payload), kib(c.Table), kib(c.Recipe), c.Objects, kib(c.Bytes()), r.exact[i])
		}
	}
	r := runs["gzip/gzip+tar+derived"]
	ch := r.ch
	data := ch.Stages[0].Data
	var cand *kinds.Bundle
	var stats *kinds.GzipStats
	t0 := time.Now()
	cand, stats, err := kinds.EncodeGzip(data, func(p []byte) (*kinds.Bundle, error) {
		t, _, err := kinds.EncodeTarLayer(p, 64<<10)
		return t, err
	})
	must(err)
	an := time.Since(t0)
	env := kinds.NewEnv(r.st)
	id := r.ids[0]
	rb := func() { env.Reconstruct(id, &counting{}) }
	rb()
	best := time.Hour
	for i := 0; i < 3; i++ {
		t0 := time.Now()
		rb()
		best = min(best, time.Since(t0))
	}
	mem := peak(rb)
	r.st.ResetReads()
	rb()
	reads, touched := r.st.Touched()
	var tb int64
	for _, n := range touched {
		tb += n
	}
	mb := func(n int64, d time.Duration) float64 { return float64(n) / (1 << 20) / d.Seconds() }
	fmt.Printf("layer (first sight): gzip %s, inflated %s, header %d B, skeleton %d B, %d blocks, %d IR units (%d inline), unit bytes %s, table %s, top-level representation %d B\n",
		kib(int64(len(data))), kib(stats.Plain), stats.HeaderBytes, stats.SkeletonBytes, stats.Blocks, stats.Units, stats.InlineUnits, kib(int64(stats.UnitBytes)), kib(int64(stats.TableBytes)), len(cand.Raw))
	fmt.Printf("analysis %v (%.1f MiB/s of gzip, %.1f MiB/s inflated) | rebuild %v (%.1f MiB/s of gzip, %.1f MiB/s inflated) | peak heap over the run %.1f MiB | whole layer touches %d objects / %s (%d reads)\n",
		an.Round(time.Millisecond), mb(int64(len(data)), an), mb(stats.Plain, an), best.Round(time.Millisecond), mb(int64(len(data)), best), mb(stats.Plain, best), mem, len(touched), kib(tb), reads)
}

func git(sc kinds.Scale, runs map[string]*run) {
	fmt.Println("\n== Git point read: one non-delta object through the contract (bytes touched by class)")
	r := runs["git/repack"]
	if r == nil {
		fmt.Println("(git producer not installed: repack chain skipped)")
		return
	}
	data := r.ch.Stages[0].Data
	p, err := gitz3.ParsePack(data, gitz3.SHA1, gitz3.Limits{})
	must(err)
	_, err = p.Resolve()
	must(err)
	id := r.ids[0]
	rep, err := kinds.NewEnv(r.st).LoadRep(id)
	must(err)
	// the Z3-20 layout: gitz3 meta in the recipe, every stream a generic STORED member
	_, gs, err := kinds.EncodeGit(data, gitz3.SHA1)
	must(err)
	old, err := kinds.LegacyGitRep(r.st, id)
	must(err)
	oldRaw, err := old.Marshal()
	must(err)
	newRaw := r.st.Bytes(id)
	fmt.Printf("pack: %d entries, %d streams, table (meta + streams) %s in %d members; top-level representation: %d B (Z3-20 flat layout: %d B, of which recipe %d B and %d members); index %d records\n",
		gs.Entries, gs.Streams, kib(int64(gs.TableBytes)), len(rep.Members), len(newRaw), len(oldRaw), len(old.Recipe), len(old.Members), rep.Indexes[0].Count)
	fmt.Printf("%-34s %9s %9s %9s %9s %9s %5s\n", "read", "top rep", "index", "table", "stream", "total", "objs")
	shown := map[string]bool{}
	for i := range p.Entries {
		if p.Entries[i].IsDelta() {
			continue
		}
		pr, err := kinds.MeasureGitRead(r.st, id, p.Entries[i].ID)
		must(err)
		kind := "single-component object"
		if pr.Table > 0 {
			kind = "multi-component object (table)"
		}
		if shown[kind] || (kind == "single-component object" && pr.Payload < 200) {
			continue
		}
		shown[kind] = true
		fmt.Printf("%-34s %9d %9d %9d %9d %9d %5d   (payload %d B)\n", "after Z3-21: "+kind, pr.Rep, pr.Index, pr.Table, pr.Stream, pr.Total, pr.Objects, pr.Payload)
		if kind == "single-component object" {
			legacy := int64(len(oldRaw)) + pr.Index + pr.Stream
			fmt.Printf("%-34s %9d %9d %9d %9d %9d %5d\n", "before (Z3-20 flat member list)", len(oldRaw), pr.Index, 0, pr.Stream, legacy, 3)
		}
	}
}

func sens(runs map[string]*run) {
	fmt.Println("\n== per-object overhead sensitivity: cumulative candidate / CDC (%) of persisted bytes + overhead x new objects")
	os := []int64{0, 32, 64, 128, 256}
	fmt.Printf("%-24s", "workload")
	for _, o := range os {
		fmt.Printf(" %6dB", o)
	}
	fmt.Println("   per-state wins while overhead below")
	for _, name := range []string{"sqlite", "git/repack", "merkle/sha3-256", "merkle-long/sha3-256", "gzip/gzip+tar+derived", "compound/tar+sqlite"} {
		r := runs[name]
		if r == nil {
			continue
		}
		var c, d repz3.Cost
		var per []string
		for i := range r.cost {
			c.Payload, c.Recipe, c.Index, c.Table, c.Objects = c.Payload+r.cost[i].Payload, c.Recipe+r.cost[i].Recipe, c.Index+r.cost[i].Index, c.Table+r.cost[i].Table, c.Objects+r.cost[i].Objects
			d.Payload, d.Recipe, d.Index, d.Table, d.Objects = d.Payload+r.cdc[i].Payload, d.Recipe+r.cdc[i].Recipe, d.Index+r.cdc[i].Index, d.Table+r.cdc[i].Table, d.Objects+r.cdc[i].Objects
			per = append(per, crossover(r.cost[i], r.cdc[i]))
		}
		fmt.Printf("%-24s", name)
		for _, o := range os {
			fmt.Printf(" %6.1f%%", 100*float64(c.Total(o))/float64(d.Total(o)))
		}
		fmt.Printf("   %s\n", strings.Join(per, " "))
	}
}

func crossover(c, d repz3.Cost) string {
	db, dn := c.Bytes()-d.Bytes(), int64(c.Objects-d.Objects)
	switch {
	case db < 0 && dn <= 0:
		return "always"
	case db >= 0 && dn >= 0:
		return "never"
	case db < 0:
		return fmt.Sprintf("<%dB", -db/dn)
	}
	return fmt.Sprintf(">%dB", db/(-dn))
}
