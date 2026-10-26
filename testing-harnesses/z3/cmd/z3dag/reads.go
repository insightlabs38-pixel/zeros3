package main

import (
	"bytes"
	"fmt"

	"zeros3-z3/carz3"
	"zeros3-z3/dagz3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

const readPage = 77

func fmtRead(label string, rc dagz3.ReadCost) {
	kind := "nested"
	if rc.Whole {
		kind = "whole "
	}
	fmt.Printf("%-40s %s reps %2d  objects %4d  rep %7d B  table %7d B  payload %7d B  index %6d B  total %8d B\n",
		label, kind, rc.Reps, rc.Objects, rc.RepBytes, rc.TableBytes, rc.PayloadBytes, rc.IdxBytes, rc.Bytes)
}

func pointRead(label string, b *kinds.Bundle, dbLen int64, want []byte) dagz3.ReadCost {
	st := repz3.NewMemStore()
	id, _, err := b.Persist(st)
	must(err)
	rc, err := dagz3.PointReadPage(st, kinds.NewEnvLimits(st, dagz3.Lim), id, dbLen, readPage, want)
	must(err)
	fmtRead(label, rc)
	return rc
}

func readsSection() {
	db := dagz3.Join(dagz3.DB(28, 160))
	want := db[readPage*4096 : (readPage+1)*4096]
	dbLen := int64(len(db))
	tarOf := func(d []byte, name string) []byte { return dagz3.Tar([]string{name}, map[string][]byte{name: d}) }

	fmt.Println("-- one page of a database, same leaf, increasing representation depth (artifact differs)")
	art := []struct {
		name string
		data []byte
	}{
		{"depth 1  sqlite", db},
		{"depth 2  tar > sqlite", tarOf(db, "app.db")},
		{"depth 3  gzip > tar > sqlite", dagz3.Gzip(tarOf(db, "app.db"), 6)},
		{"depth 4  zstd > tar > gzip > sqlite", dagz3.Zstd(tarOf(dagz3.Gzip(db, 6), "app.db.gz"))},
	}
	for _, a := range art {
		b, err := dagz3.Discover(a.data, dagz3.DefaultBudget(4))
		must(err)
		pointRead(fmt.Sprintf("%s (%d)", a.name, dagz3.Depth(b)), b, dbLen, want)
	}
	fmt.Println("-- same depth-4 artifact, capped candidates (what the structure exposes)")
	chain := art[3].data
	for _, k := range caps {
		b, err := dagz3.Discover(chain, dagz3.DefaultBudget(k))
		must(err)
		pointRead(fmt.Sprintf("cap %d (depth %d)", k, dagz3.Depth(b)), b, dbLen, want)
	}

	fmt.Println("-- one block of a CAR, nested (gzip > tar > zstd > car)")
	car := dagz3.CARAdd(0)
	rep, _, err := carz3.Build(bytes.NewReader(car), int64(len(car)), carz3.Options{Limits: carz3.DefaultLimits()})
	must(err)
	var cid []byte
	for i := range rep.Refs {
		if rep.Refs[i].Inline == nil {
			cid = rep.CIDBytes(i)
			break
		}
	}
	for _, a := range []struct {
		name string
		data []byte
		cap  int
	}{{"depth 1 car", car, 4}, {"depth 4 gzip>tar>zstd>car", dagz3.ChainB(0), 4}, {"same, cap 3 (car not exposed)", dagz3.ChainB(0), 3}} {
		b, err := dagz3.Discover(a.data, dagz3.DefaultBudget(a.cap))
		must(err)
		st := repz3.NewMemStore()
		id, _, err := b.Persist(st)
		must(err)
		env := kinds.NewEnvLimits(st, dagz3.Lim)
		st.ResetReads()
		c, ok, err := descendKind(env, id, repz3.KindCAR)
		must(err)
		if !ok {
			_, _, err := env.Reconstruct(id, discardW{})
			must(err)
			_, touched := st.Touched()
			var n int64
			for _, v := range touched {
				n += v
			}
			fmt.Printf("%-40s whole  objects %4d  total %8d B\n", a.name, len(touched), n)
			continue
		}
		blk, err := kinds.ReadCARBlock(c, cid)
		must(err)
		_, touched := st.Touched()
		var n int64
		for _, v := range touched {
			n += v
		}
		fmt.Printf("%-40s nested objects %4d  total %8d B  (block %d B, car %d B)\n", a.name, len(touched), n, len(blk), len(car))
	}
}

type discardW struct{}

func (discardW) Write(p []byte) (int, error) { return len(p), nil }

// descendKind opens the first representation of the given kind below root through Child calls.
func descendKind(env *repz3.Env, root repz3.SHA, k repz3.Kind) (*repz3.Call, bool, error) {
	var walk func(c *repz3.Call) (*repz3.Call, error)
	walk = func(c *repz3.Call) (*repz3.Call, error) {
		if c.Rep.Kind == k {
			return c, nil
		}
		for i, m := range c.Rep.Members {
			if m.Role == repz3.Derived {
				ch, err := c.Child(i)
				if err != nil {
					return nil, err
				}
				if got, err := walk(ch); err != nil || got != nil {
					return got, err
				}
			}
		}
		return nil, nil
	}
	c, err := env.Open(root)
	if err != nil {
		return nil, false, err
	}
	got, err := walk(c)
	return got, got != nil, err
}
