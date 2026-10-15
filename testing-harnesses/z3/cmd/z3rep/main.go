// Command z3rep prints the Z3-20 measurements of the serialized representation
// contract over the migrated workloads (CAR, SQLite, Git, Merkle, tar+SQLite).
//
//	z3rep [-scale small|full] [-only cost,sens,reads,perf,cover]
//
// Everything is derived from bytes the contract persists; the Git repack chain
// needs the optional git producer and is skipped without it.
package main

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"zeros3-z3/carz3"
	"zeros3-z3/gitz3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

var overheads = []int64{0, 16, 32, 64, 128, 256}

type row struct {
	chain, stage string
	data         int
	cand, cdc    repz3.Cost
	id           repz3.SHA
	exact        bool
}

func main() {
	scale := flag.String("scale", "full", "small|full")
	only := flag.String("only", "cost,sens,reads,perf,cover", "sections")
	flag.Parse()
	sc := kinds.Full
	if *scale == "small" {
		sc = kinds.Small
	}
	work, _ := os.MkdirTemp("", "z3rep")
	defer os.RemoveAll(work)
	chains, err := kinds.Chains(sc, work)
	if err != nil {
		fmt.Fprintln(os.Stderr, "z3rep:", err)
		os.Exit(1)
	}
	want := map[string]bool{}
	for _, s := range strings.Split(*only, ",") {
		want[s] = true
	}
	var rows []row
	stores := map[string]*repz3.MemStore{}
	first := map[string]repz3.SHA{} // chain -> representation of its first stage
	for _, ch := range chains {
		st, cst := repz3.NewMemStore(), repz3.NewMemStore()
		stores[ch.Name] = st
		for _, s := range ch.Stages {
			cand, err := ch.Encode(st, s.Data)
			if err != nil {
				fmt.Fprintf(os.Stderr, "z3rep: %s/%s: %v\n", ch.Name, s.Label, err)
				os.Exit(1)
			}
			id, cc, err := kinds.Commit(st, cand)
			must(err)
			var out bytes.Buffer
			n, sum, err := kinds.NewEnv(st).Reconstruct(id, &out)
			ok := err == nil && n == int64(len(s.Data)) && sum == sha256.Sum256(s.Data) && bytes.Equal(out.Bytes(), s.Data)
			cdc, err := kinds.CDC(s.Data)
			must(err)
			_, dc, err := kinds.Commit(cst, cdc)
			must(err)
			if _, seen := first[ch.Name]; !seen {
				first[ch.Name] = id
			}
			rows = append(rows, row{ch.Name, s.Label, len(s.Data), cc, dc, id, ok})
		}
	}
	if want["cost"] {
		cost(rows)
	}
	if want["sens"] {
		sens(rows)
	}
	if want["reads"] {
		reads(chains, stores, first)
	}
	if want["cover"] {
		cover(chains, stores, first)
	}
	if want["perf"] {
		perf(chains, stores, first)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "z3rep:", err)
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

func cost(rows []row) {
	fmt.Println("== cost: bytes the contract persisted for each state added to a store holding the earlier ones (candidate | CDC as KindConcat)")
	fmt.Printf("%-20s %-18s %8s | %9s %7s %6s %5s | %9s %6s %5s | exact\n", "workload", "state", "logical", "payload", "recipe", "index", "objs", "payload", "recipe", "objs")
	for _, r := range rows {
		fmt.Printf("%-20s %-18s %8s | %9s %7s %6s %5d | %9s %6s %5d | %v\n", r.chain, r.stage, kib(int64(r.data)),
			kib(r.cand.Payload), kib(r.cand.Recipe), kib(r.cand.Index), r.cand.Objects, kib(r.cdc.Payload), kib(r.cdc.Recipe), r.cdc.Objects, r.exact)
	}
}

func total(c repz3.Cost, o int64) int64 { return c.Total(o) }

func crossover(c, d repz3.Cost) string {
	db, dn := c.Bytes()-d.Bytes(), int64(c.Objects-d.Objects)
	switch {
	case db < 0 && dn <= 0:
		return "always"
	case db >= 0 && dn >= 0:
		return "never"
	case db < 0: // fewer bytes, more objects: wins until the overhead eats the saving
		return fmt.Sprintf("<%d B", -db/dn)
	}
	return fmt.Sprintf(">%d B", db/(-dn)) // more bytes, fewer objects
}

func sens(rows []row) {
	fmt.Println("\n== sensitivity: total = persisted bytes + overhead x new objects; cumulative candidate / CDC (%), and the per-state crossover overhead")
	fmt.Printf("%-20s", "workload")
	for _, o := range overheads {
		fmt.Printf(" %6dB", o)
	}
	fmt.Println("   cumulative crossover | per-state wins at <overhead")
	byChain := map[string][]row{}
	var order []string
	for _, r := range rows {
		if _, ok := byChain[r.chain]; !ok {
			order = append(order, r.chain)
		}
		byChain[r.chain] = append(byChain[r.chain], r)
	}
	for _, name := range order {
		var c, d repz3.Cost
		var per []string
		for _, r := range byChain[name] {
			c.Payload, c.Recipe, c.Index, c.Objects = c.Payload+r.cand.Payload, c.Recipe+r.cand.Recipe, c.Index+r.cand.Index, c.Objects+r.cand.Objects
			d.Payload, d.Recipe, d.Index, d.Objects = d.Payload+r.cdc.Payload, d.Recipe+r.cdc.Recipe, d.Index+r.cdc.Index, d.Objects+r.cdc.Objects
			per = append(per, crossover(r.cand, r.cdc))
		}
		fmt.Printf("%-20s", name)
		for _, o := range overheads {
			fmt.Printf(" %6.1f%%", 100*float64(total(c, o))/float64(total(d, o)))
		}
		fmt.Printf("   %-10s | %s\n", crossover(c, d), strings.Join(per, " "))
	}
}

func reads(chains []*kinds.Chain, stores map[string]*repz3.MemStore, first map[string]repz3.SHA) {
	fmt.Println("\n== partial reads through the contract (objects and bytes read; representation = the serialized object, always read whole)")
	line := func(what string, st *repz3.MemStore, id repz3.SHA, logical int64) {
		_, tb := st.Touched()
		r, err := kinds.NewEnv(st).Reachable(id)
		must(err)
		var rec, idx, pay int64
		var no int
		for obj, n := range tb {
			no++
			switch r.Role[obj] {
			case repz3.DepRep:
				rec += n
			case repz3.DepIndex:
				idx += n
			default:
				pay += n
			}
		}
		fmt.Printf("%-24s logical %6d B | objects %3d | component bytes %8d | recipe %8d | index %6d\n", what, logical, no, pay, rec, idx)
	}
	for _, ch := range chains {
		st := stores[ch.Name]
		s := ch.Stages[0]
		env := kinds.NewEnv(st)
		id := first[ch.Name]
		switch {
		case ch.Name == "sqlite":
			st.ResetReads()
			c, _ := env.Open(id)
			_, err := kinds.ReadSQLitePage(c, int64(len(s.Data)/4096/2))
			must(err)
			line("sqlite one 4 KiB page", st, id, 4096)
		case ch.Name == "car/v1-reorder":
			rd, _ := carz3.NewReader(bytes.NewReader(s.Data), carz3.DefaultLimits())
			var cid []byte
			var n int64
			for i := 0; i < 40; i++ {
				e, _ := rd.Next()
				cid, n = e.CID.Raw, int64(len(e.Data))
			}
			st.ResetReads()
			c, _ := env.Open(id)
			_, err := kinds.ReadCARBlock(c, cid)
			must(err)
			line("car one block", st, id, n)
		case ch.Name == "git/repack":
			p, _ := gitz3.ParsePack(s.Data, gitz3.SHA1, gitz3.Limits{})
			p.Resolve()
			for i := range p.Entries {
				if p.Entries[i].IsDelta() || p.Entries[i].Size < 200 {
					continue
				}
				st.ResetReads()
				c, _ := env.Open(id)
				_, pay, err := kinds.ReadGitObject(c, p.Entries[i].ID)
				must(err)
				line("git one native object", st, id, int64(len(pay)))
				break
			}
		case ch.Name == "compound/tar+sqlite":
			st.ResetReads()
			c, _ := env.Open(id)
			for i, m := range c.Rep.Members {
				if m.Role == repz3.Derived {
					cc, err := c.Child(i)
					must(err)
					if cc.Rep.Kind != repz3.KindSQLite {
						continue
					}
					_, err = kinds.ReadSQLitePage(cc, 100)
					must(err)
				}
			}
			line("compound inner page", st, id, 4096)
		case strings.HasPrefix(ch.Name, "merkle/"):
			fmt.Printf("%-24s whole archive only: a snapshot is one DFS stream; no sub-range is addressable without a node-id read path (not built)\n", "merkle range")
		}
	}
}

func cover(chains []*kinds.Chain, stores map[string]*repz3.MemStore, first map[string]repz3.SHA) {
	fmt.Println("\n== coverage of the first migrated stage: members, VIA objects, recipe and index bytes, derived members and frames")
	fmt.Printf("%-20s %7s %7s %6s %8s %8s %6s %6s %8s %5s | %9s %8s %9s\n", "workload", "stored", "via", "reps", "recipeB", "indexB", "index#", "deriv", "frameB", "kind", "walkRead", "walk", "viaOnWire")
	for _, ch := range chains {
		st := stores[ch.Name]
		id := first[ch.Name]
		env := kinds.NewEnv(st)
		rep, err := env.LoadRep(id)
		must(err)
		st.ResetReads()
		t0 := time.Now()
		r, err := env.Reachable(id)
		walk := time.Since(t0)
		must(err)
		_, tb := st.Touched()
		var walkRead int64
		for _, n := range tb {
			walkRead += n
		}
		var frame, idxB int64
		var idxN int64
		var recipe int
		derived := 0
		var visit func(rp *repz3.Rep)
		visit = func(rp *repz3.Rep) {
			recipe += len(rp.Recipe)
			for _, x := range rp.Indexes {
				idxB += x.Bytes()
				idxN += x.Count
			}
			for _, m := range rp.Members {
				if m.Role == repz3.Derived {
					derived++
					frame += int64(len(m.Frame.Prefix) + m.Frame.Zeros)
					cr, err := env.LoadRep(m.Child)
					must(err)
					visit(cr)
				}
			}
		}
		visit(rep)
		fmt.Printf("%-20s %7d %7d %6d %8d %8d %6d %6d %8d %5d | %9d %8s %9d\n", ch.Name, r.Count(repz3.DepStored), r.Count(repz3.DepVia), r.Count(repz3.DepRep), recipe, idxB, idxN, derived, frame, rep.Kind,
			walkRead, walk.Round(10*time.Microsecond), 33*r.Count(repz3.DepVia))
	}
}

type counting struct{ n int64 }

func (c *counting) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

func timeIt(f func()) (per time.Duration) {
	f()
	n := 1
	for {
		t0 := time.Now()
		for i := 0; i < n; i++ {
			f()
		}
		el := time.Since(t0)
		if el > 400*time.Millisecond || n >= 1<<12 {
			return el / time.Duration(n)
		}
		n *= 4
	}
}

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
	hi := <-done
	return float64(int64(hi)-int64(base.HeapAlloc)) / (1 << 20)
}

func perf(chains []*kinds.Chain, stores map[string]*repz3.MemStore, first map[string]repz3.SHA) {
	buf := make([]byte, 64<<20)
	shaMBs := func() float64 {
		d := timeIt(func() { sha256.Sum256(buf) })
		return float64(len(buf)) / (1 << 20) / d.Seconds()
	}()
	fmt.Printf("\n== contract overhead (SHA-256 on this machine: %.0f MB/s). Marshal/Unmarshal of the representation object; Reconstruct through the contract vs the pre-contract direct path (same stored bytes) without and with the same SHA-256 verification\n", shaMBs)
	fmt.Printf("%-20s %9s %10s %10s | %9s %9s %9s %7s %7s | %8s\n", "workload", "repBytes", "marshal", "unmarshal", "contract", "direct", "direct+H", "c/d", "c/d+H", "peakMiB")
	for _, ch := range chains {
		if len(ch.Stages) == 0 || strings.HasPrefix(ch.Name, "git/fixture") {
			continue
		}
		st := stores[ch.Name]
		id := first[ch.Name]
		env := kinds.NewEnv(st)
		rep, _ := env.LoadRep(id)
		raw := st.Bytes(id)
		mt := timeIt(func() { rep.Marshal() })
		ut := timeIt(func() { repz3.Unmarshal(raw, repz3.Limits{}) })
		logical := ch.Stages[0].Data
		ct := timeIt(func() { env.Reconstruct(id, io.Discard) })
		pk := peak(func() { env.Reconstruct(id, io.Discard) })
		mbs := func(d time.Duration) float64 { return float64(len(logical)) / (1 << 20) / d.Seconds() }
		raw1, raw2 := directPath(ch, st, rep, false), directPath(ch, st, rep, true)
		dtxt, htxt, r1, r2 := "n/a", "n/a", "-", "-"
		if raw1 != nil {
			dt, ht := timeIt(raw1), timeIt(raw2)
			dtxt, htxt = fmt.Sprintf("%.0fMB/s", mbs(dt)), fmt.Sprintf("%.0fMB/s", mbs(ht))
			r1, r2 = fmt.Sprintf("%.2fx", float64(ct)/float64(dt)), fmt.Sprintf("%.2fx", float64(ct)/float64(ht))
		}
		fmt.Printf("%-20s %9d %7.0fMB/s %7.0fMB/s | %7.0fMB/s %9s %9s %7s %7s | %8.1f\n", ch.Name, len(raw),
			float64(len(raw))/(1<<20)/mt.Seconds(), float64(len(raw))/(1<<20)/ut.Seconds(), mbs(ct), dtxt, htxt, r1, r2, pk)
	}
}
