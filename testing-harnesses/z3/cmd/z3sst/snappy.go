package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"zeros3-z3/snappyz3"
	"zeros3-z3/sstz3"
)

// cmdSnappy measures exact Snappy handling over a corpus: for every Snappy block, does
// the deterministic reference encoder reproduce the stored bytes (one flag in the
// recipe) or does the block need its explicit command recipe, and what do both cost.
func cmdSnappy(args []string) error {
	type agg struct {
		blocks, big           int
		pred                  [4]int
		bytes, hitBytes       int64
		recipe, cmds          int64
		allExplicit           int64
		decode, replay, pred_ time.Duration
		decBytes              int64
	}
	res := map[string]*agg{}
	for _, p := range tableFiles(args) {
		prod := "?"
		for _, k := range []string{"leveldb", "rocksdb", "pebble"} {
			if strings.Contains(p, "/"+k+"/") {
				prod = k
			}
		}
		a := res[prod]
		if a == nil {
			a = &agg{}
			res[prod] = a
		}
		f, _ := os.Open(p)
		st, _ := f.Stat()
		t, err := sstz3.Open(f, st.Size(), sstz3.Limits{})
		if err != nil || t.Reject() != nil {
			f.Close()
			continue
		}
		for _, h := range t.Blocks {
			rb, err := sstz3.ReadBlock(f, t.Size, h, t.Footer.Checksum, false, sstz3.Limits{})
			if err != nil || rb.Comp() != sstz3.CompSnappy {
				continue
			}
			t0 := time.Now()
			dec, cmds, err := snappyz3.Parse(rb.Payload, snappyz3.Limits{})
			a.decode += time.Since(t0)
			if err != nil {
				return err
			}
			a.blocks++
			a.bytes += int64(len(rb.Payload))
			a.decBytes += int64(len(dec))
			rec := snappyz3.Marshal(cmds)
			a.allExplicit += int64(len(rec)) + 1
			t1 := time.Now()
			var b bytes.Buffer
			snappyz3.Replay(&b, dec, cmds)
			a.replay += time.Since(t1)
			if len(dec) >= snappyz3.MaxPredict {
				a.big++
			}
			t2 := time.Now()
			which := 0
			if len(dec) <= snappyz3.MaxPredict {
				for p, f := range [...]func([]byte) []byte{1: snappyz3.EncodeRef, 2: snappyz3.EncodeCPP, 3: snappyz3.EncodeCPP15} {
					if f != nil && bytes.Equal(f(dec), rb.Payload) {
						which = p
						break
					}
				}
			}
			a.pred[which]++
			a.pred_ += time.Since(t2)
			if which != 0 {
				a.hitBytes += int64(len(rb.Payload))
			} else {
				a.recipe += int64(len(rec)) + 1
			}
			a.cmds += int64(len(cmds))
		}
		f.Close()
	}
	for _, k := range []string{"leveldb", "rocksdb", "pebble"} {
		a := res[k]
		if a == nil {
			continue
		}
		fmt.Printf("%-8s snappy blocks=%d explicit/go/cpp14/cpp15=%v predicted %.3f%% of blocks, %.3f%% of bytes; explicit-recipe fallback %.2f MiB (%.3f%% of snappy bytes); all-explicit would cost %.2f MiB; cmds/block=%.0f; parse %.0f MiB/s, command replay %.0f MiB/s, predictors %.0f MiB/s (all three tried)\n",
			k, a.blocks, a.pred, 100*float64(a.blocks-a.pred[0])/float64(a.blocks), 100*float64(a.hitBytes)/float64(a.bytes), float64(a.recipe)/(1<<20), 100*float64(a.recipe)/float64(a.bytes),
			float64(a.allExplicit)/(1<<20), float64(a.cmds)/float64(a.blocks), float64(a.bytes)/(1<<20)/a.decode.Seconds(), float64(a.decBytes)/(1<<20)/a.replay.Seconds(), float64(a.decBytes)/(1<<20)/a.pred_.Seconds())
	}
	return nil
}
