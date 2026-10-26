package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"zeros3-z3/carz3"
	"zeros3-z3/gitz3"
	"zeros3-z3/prodshadow"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

type readRow struct {
	name  string
	auth  prodshadow.Cost
	shad  prodshadow.Cost
	exact bool
}

func (r readRow) print(state string) {
	fmt.Printf("%-7s %-22s | CDC %3d chunks %3d recs %2d packs %-18s %9d B | shadow %3d chunks %3d recs %2d packs %-18s %9d B (%d ReadAt) | exact=%v\n",
		state, r.name, r.auth.Chunks, r.auth.Records, r.auth.Packs, r.auth.Tiers, r.auth.StoredBytes,
		r.shad.Chunks, r.shad.Records, r.shad.Packs, r.shad.Tiers, r.shad.StoredBytes, r.shad.Calls, r.exact)
}

// reads measures, per workload, the same logical read through the ordinary CDC path
// and through the shadow representation, on the final store of each chain: packed
// (production compaction), then with packs spread over hot/warm/cold.
func (c config) reads() {
	chs, err := c.workloads()
	must(err)
	fmt.Println("== reads: authoritative CDC range/whole GET vs shadow logical read (stored bytes = pack payloads as stored + loose files)")
	for _, ch := range chs {
		src := &prodshadow.Lab{B: c.b, Base: filepath.Join(c.work, "chain-"+filepath.Base(ch.Name), "Q")}
		src.Store = &prodshadow.Store{B: c.b, Dir: filepath.Join(src.Base, "store")}
		src.Meta = &prodshadow.Meta{Dir: filepath.Join(src.Base, "shadow")}
		dst := filepath.Join(c.work, "reads-"+filepath.Base(ch.Name))
		os.RemoveAll(dst)
		l, err := prodshadow.NewLab(c.b, dst, prodshadow.Options{})
		must(err)
		must(prodshadow.CopyDir(src.Store.Dir, l.Store.Dir))
		must(prodshadow.CopyDir(src.Meta.Dir, l.Meta.Dir))
		must(l.Start())
		live := filepath.Join(dst, "live.txt")
		_, err = l.LiveFile(live)
		must(err)
		must(l.Stop())
		_, err = l.Store.Compact(c.packMiB, true, live)
		must(err)
		for _, state := range []string{"packed", "tiered"} {
			if state == "tiered" {
				spread, err := l.Store.SpreadPacks()
				must(err)
				fmt.Printf("   %s packs per tier: %v\n", ch.Name, spread)
			}
			must(l.Start())
			rows := c.measureReads(l, ch)
			must(l.Stop())
			for _, r := range rows {
				r.name = ch.Name[strings.LastIndex(ch.Name, "/")+1:] + ": " + r.name
				r.print(state)
			}
		}
	}
}

func (c config) measureReads(l *prodshadow.Lab, ch *kinds.Chain) []readRow {
	data := ch.Stages[len(ch.Stages)-1].Data
	roots, _ := l.Sh.Adopt()
	var rep repz3.SHA
	want := sha256.Sum256(data)
	for _, r := range roots {
		if r.ObjSHA == want {
			rep = r.Rep
		}
	}
	if rep == (repz3.SHA{}) {
		must(fmt.Errorf("%s: no shadow root for the final object", ch.Name))
	}
	var rows []readRow
	add := func(name string, off, n int64, fn func(c *repz3.Call) ([]byte, error), expect []byte) {
		a, err := l.AuthCost("obj", off, n)
		must(err)
		var got []byte
		s, err := l.ShadowCost(func() error {
			call, err := l.Sh.Env().Open(rep)
			if err != nil {
				return err
			}
			got, err = fn(call)
			return err
		})
		must(err)
		rows = append(rows, readRow{name: name, auth: a, shad: s, exact: bytes.Equal(got, expect)})
	}
	switch {
	case strings.HasPrefix(ch.Name, "sqlite"):
		n := int64(len(data)/4096) / 2
		add(fmt.Sprintf("page %d (4 KiB)", n), n*4096, 4096, func(call *repz3.Call) ([]byte, error) { return kinds.ReadSQLitePage(call, n) }, data[n*4096:(n+1)*4096])
	case strings.HasPrefix(ch.Name, "git"):
		p, err := gitz3.ParsePack(data, gitz3.SHA1, gitz3.Limits{})
		must(err)
		_, rerr := p.Resolve()
		must(rerr)
		for i := len(p.Entries) / 2; i < len(p.Entries); i++ {
			e := p.Entries[i]
			if e.IsDelta() {
				continue
			}
			want, err := p.Inflate(i)
			must(err)
			add(fmt.Sprintf("git object %.8x (%d B entry)", e.ID, e.End-e.Off), e.Off, e.End-e.Off, func(call *repz3.Call) ([]byte, error) {
				_, payload, err := kinds.ReadGitObject(call, e.ID)
				return payload, err
			}, want)
			break
		}
	case strings.HasPrefix(ch.Name, "car"):
		rd, err := carz3.NewReader(bytes.NewReader(data), carz3.DefaultLimits())
		must(err)
		var e *carz3.Entry
		for i := 0; i < 20; i++ {
			e, err = rd.Next()
			must(err)
		}
		cid, block := e.CID.Raw, append([]byte(nil), e.Data...)
		add(fmt.Sprintf("car block (%d B)", len(block)), e.Off, e.End()-e.Off, func(call *repz3.Call) ([]byte, error) { return kinds.ReadCARBlock(call, cid) }, block)
	}
	// whole object
	var got bytes.Buffer
	t0 := time.Now()
	a, err := l.AuthCost("obj", 0, -1)
	must(err)
	authT := time.Since(t0)
	s, err := l.ShadowCost(func() error {
		_, _, err := l.Sh.Env().Reconstruct(rep, &got)
		return err
	})
	must(err)
	rows = append(rows, readRow{name: fmt.Sprintf("whole layer (%.1f MiB) %v vs %v", float64(len(data))/(1<<20), authT.Round(time.Millisecond), s.Elapsed.Round(time.Millisecond)), auth: a, shad: s, exact: bytes.Equal(got.Bytes(), data)})
	return rows
}
