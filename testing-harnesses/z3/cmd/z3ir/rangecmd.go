package main

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand"
	"os"

	"zeros3-z3/deflate"
	"zeros3-z3/ir"
)

// rangeCmd: Z3-08G range-read study on real gzip files. For random compressed
// byte ranges it materializes the bytes from a unit index and reports what a
// reader would have to fetch.
func rangeCmd(args []string) {
	fs := flag.NewFlagSet("range", flag.ExitOnError)
	files := fs.String("gz", "", "comma-separated .gz files")
	target := fs.Int("target", 16<<10, "unit target bytes")
	trials := fs.Int("trials", 40, "random ranges per size")
	profile := fs.Int("profile", int(ir.AutoProfile), "predictor profile")
	fs.Parse(args)
	for _, f := range split(*files) {
		b, err := os.ReadFile(f)
		if err != nil {
			fatal(err)
		}
		br := bytes.NewReader(b)
		hdr, err := deflate.GzipHeader(br)
		if err != nil {
			fatal(err)
		}
		var sink memSinkCmd
		ss, _, err := ir.EncodeStream(br, ir.StreamConfig{Target: *target, Profile: uint8(*profile)}, &sink)
		if err != nil {
			fatal(err)
		}
		raw := b[len(hdr) : len(hdr)+int(ss.StreamLen)]
		ix, err := ss.Index()
		if err != nil {
			fatal(err)
		}
		plain := sink.plain.Bytes()
		plainAt := func(from, to int64) ([]byte, error) { return plain[from:to], nil }
		unit := func(i int) ([]byte, error) { return sink.units[i], nil }
		fmt.Printf("\n### %s: %.1f MiB compressed, %.1f MiB plaintext, %d units, %d blocks; skeleton %.0f KiB (%.2f%% of the stream), units %.0f KiB (%.2f%%)\n\n",
			f, float64(len(raw))/(1<<20), float64(len(plain))/(1<<20), ss.Units, ss.Blocks, float64(len(ss.Skeleton))/1024, 100*float64(len(ss.Skeleton))/float64(len(raw)),
			float64(ss.UnitBytes)/1024, 100*float64(ss.UnitBytes)/float64(len(raw)))
		fmt.Println("| index | index bytes | % of stream | range | units decoded (avg) | units only for a predicted header | unit bytes fetched | plaintext fetched | fetched / range | exact |")
		fmt.Println("|---|---:|---:|---|---:|---:|---:|---:|---:|---|")
		r := rand.New(rand.NewSource(11))
		for _, step := range []int{1, 16, 128} {
			sp := ix.Checkpoints(step)
			ib := len(sp.Marshal())
			for _, sz := range []int64{4 << 10, 64 << 10, 1 << 20, 16 << 20} {
				if sz > int64(len(raw))/2 {
					continue
				}
				var units, hdrU, ub, pb int
				ok := true
				for i := 0; i < *trials; i++ {
					a := r.Int63n(int64(len(raw)) - sz)
					got, need, err := ir.MaterializeRange(ss.Skeleton, sp, step, unit, plainAt, a, a+sz)
					if err != nil || !bytes.Equal(got, raw[a:a+sz]) {
						ok = false
						if err != nil {
							fmt.Fprintln(os.Stderr, "range error:", err)
						}
						continue
					}
					units += need.UnitsDecoded
					hdrU += need.HeaderUnits
					ub += need.UnitBytes
					pb += int(need.PlainBytes)
				}
				n := float64(*trials)
				fmt.Printf("| every %d unit(s) | %d | %.3f | %s | %.1f | %.1f | %.1f KiB | %.1f KiB | %.1fx | %v |\n", step, ib, 100*float64(ib)/float64(len(raw)), sizeName(sz),
					float64(units)/n, float64(hdrU)/n, float64(ub)/n/1024, float64(pb)/n/1024, float64(pb)/n/float64(sz), ok)
			}
		}
	}
}

func sizeName(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%d MiB", n>>20)
	default:
		return fmt.Sprintf("%d KiB", n>>10)
	}
}

type memSinkCmd struct {
	plain bytes.Buffer
	units [][]byte
}

func (m *memSinkCmd) Plain(p []byte) error { m.plain.Write(p); return nil }
func (m *memSinkCmd) Unit(i int, d []byte) error {
	m.units = append(m.units, append([]byte(nil), d...))
	return nil
}
