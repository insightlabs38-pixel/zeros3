package main

import (
	"bytes"
	"fmt"
	"runtime"
	"time"

	"crypto/sha256"
	"zeros3-z3/lab"
	"zeros3-z3/tdelta"
)

// policy cost of one file: bytes stored and the files that must be decoded first.
type fileCost struct {
	base  int // -1 = stored without a base
	bytes int64
	depth int
	dec   time.Duration // decode time of the whole dependency path
}

func runFamily(cks map[string][]*ckpt, order []string, minReuse int, oracle bool, maxFrac float64) {
	fmt.Println("| family | policy | physical % of all files | mean depth | max depth | restore latest (decode, s) | note |")
	fmt.Println("|---|---|---:|---:|---:|---:|---|")
	for _, f := range order {
		c := cks[f]
		if len(c) < 3 {
			continue
		}
		cache := map[[2]int]*pairRes{}
		get := func(i, j int) *pairRes { // i = base index, -1 = none
			k := [2]int{i, j}
			if r := cache[k]; r != nil {
				return r
			}
			var b *ckpt
			if i >= 0 {
				b = c[i]
			}
			r := evalPair(b, c[j], minReuse, maxFrac)
			if !r.exact {
				fatal(fmt.Errorf("RECONSTRUCTION MISMATCH %s %d<-%d", f, j, i))
			}
			cache[k] = r
			return r
		}
		var raw int64
		for _, x := range c {
			raw += int64(len(x.data))
		}
		// raw CDC: every file against the union of the earlier ones
		seen := map[[32]byte]bool{}
		var cdc int64
		for _, x := range c {
			lab.ChunkCDC(bytes.NewReader(x.data), func(_ int64, ch []byte) error {
				h := sha256.Sum256(ch)
				if !seen[h] {
					seen[h] = true
					cdc += int64(len(ch))
				}
				return nil
			})
		}
		fmt.Printf("| %s | raw CDC (all earlier files) | %s | 0 | 0 | - | |\n", short(f), pct(cdc, raw))
		a0 := get(-1, 0)
		anchorBytes := a0.hybrid
		report := func(name, note string, costs []fileCost) {
			var tot int64
			var dsum, dmax int
			for _, x := range costs {
				tot += x.bytes
				dsum += x.depth
				if x.depth > dmax {
					dmax = x.depth
				}
			}
			fmt.Printf("| %s | %s | %s | %.1f | %d | %.2f | %s |\n", short(f), name, pct(tot, raw), float64(dsum)/float64(len(costs)), dmax, costs[len(costs)-1].dec.Seconds(), note)
		}
		mk := func(pick func(j int, costs []fileCost) int) []fileCost {
			costs := []fileCost{{base: -1, bytes: anchorBytes, depth: 0, dec: a0.dec}}
			for j := 1; j < len(c); j++ {
				b := pick(j, costs)
				r := get(b, j)
				d := 1 + costs[b].depth
				costs = append(costs, fileCost{base: b, bytes: r.hybrid, depth: d, dec: costs[b].dec + r.dec})
			}
			return costs
		}
		report("anchor (all vs first)", "one level", mk(func(j int, _ []fileCost) int { return 0 }))
		report("chain (each vs previous)", "unbounded depth", mk(func(j int, _ []fileCost) int { return j - 1 }))
		for _, D := range []int{2, 3} {
			D := D
			report(fmt.Sprintf("bounded D=%d: min(previous if depth<%d, first)", D, D), "per-file cheaper of two bases", mk(func(j int, costs []fileCost) int {
				if costs[j-1].depth >= D || j-1 == 0 {
					return 0
				}
				if get(j-1, j).hybrid <= get(0, j).hybrid {
					return j - 1
				}
				return 0
			}))
		}
		if oracle {
			report("oracle (any earlier file, unbounded)", "diagnostic only", mk(func(j int, costs []fileCost) int {
				best, bi := int64(1<<62), 0
				for i := 0; i < j; i++ {
					if h := get(i, j).hybrid; h < best {
						best, bi = h, i
					}
				}
				return bi
			}))
		}
		fmt.Printf("| %s | standalone only (no base) | %s | 0 | 0 | - | |\n", short(f), func() string {
			var t int64
			for j := range c {
				t += get(-1, j).hybrid
			}
			return pct(t, raw)
		}())
	}
}

// ---- row-reuse threshold sweep ----

func runSweep(cks map[string][]*ckpt, order []string) {
	fmt.Println("| family | pair | delta only % | rows min-reuse=1 | 2 | 4 | 8 | 32 | bytes saved by rows (min 1) | rows chosen (min 1) | row recipe B (min 1) | avg reuse run (rows) |")
	fmt.Println("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, f := range order {
		c := cks[f]
		for j := 1; j < len(c); j++ {
			for _, i := range func() []int {
				if j > 1 {
					return []int{j - 1, 0}
				}
				return []int{0}
			}() {
				var raw, delta int64
				rows := map[int]int64{}
				var chosen, meta int
				var reuseRows, runs int
				for k := range c[j].f.Tensors {
					t := &c[j].f.Tensors[k]
					bt, ok := c[i].f.Lookup(t.Name)
					if !ok || bt.DType != t.DType || bt.Size() != t.Size() {
						continue
					}
					tg := c[j].data[c[j].f.DataStart+t.Begin : c[j].f.DataStart+t.End]
					bs := c[i].data[c[i].f.DataStart+bt.Begin : c[i].f.DataStart+bt.End]
					sp := tdelta.Spec{Shape: t.Shape, W: tdelta.ElemWidth(t)}
					raw += int64(len(tg))
					_, cd, _ := tdelta.EncodeTensor(sp, bs, tg, tdelta.Options{NoRows: true})
					base := tdelta.Cand{}
					for _, x := range cd {
						base = pickBest(base, x)
					}
					delta += int64(base.Wire)
					for _, mr := range []int{1, 2, 4, 8, 32} {
						_, cr, _ := tdelta.EncodeTensor(sp, bs, tg, tdelta.Options{MinReuse: mr})
						best := tdelta.Cand{}
						for _, x := range cr {
							best = pickBest(best, x)
						}
						rows[mr] += int64(best.Wire)
						if mr == 1 && best.Mode == tdelta.ModeRows {
							chosen++
							meta += best.Meta
							reuseRows += best.Rows.ReuseRows
							runs += best.Rows.Runs
						}
					}
				}
				avg := "-"
				if runs > 0 {
					avg = fmt.Sprintf("%.1f", float64(reuseRows)/float64(runs/2+1))
				}
				fmt.Printf("| %s | %s -> %s | %s | %s | %s | %s | %s | %s | %d | %d | %d | %s |\n", short(f), c[i].label, c[j].label, pct(delta, raw),
					pct(rows[1], raw), pct(rows[2], raw), pct(rows[4], raw), pct(rows[8], raw), pct(rows[32], raw), delta-rows[1], chosen, meta, avg)
			}
		}
	}
}

func pickBest(a, b tdelta.Cand) tdelta.Cand {
	if b.Wire > 0 && (a.Wire == 0 || b.Wire < a.Wire) {
		return b
	}
	return a
}

// ---- performance ----

type meas struct {
	name  string
	bytes int64
	enc   time.Duration
	dec   time.Duration
	encA  uint64 // allocated bytes
	decA  uint64
	encN  uint64 // allocations
	decN  uint64
	out   int64
}

func runPerf(cks map[string][]*ckpt, order []string, i, j, minReuse int) {
	fmt.Println("| family | pair | codec | tensor MiB | out % | encode MiB/s | decode MiB/s | enc alloc / MiB | dec alloc / MiB | enc allocs | dec allocs |")
	fmt.Println("|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, f := range order {
		c := cks[f]
		if j >= len(c) || i >= j {
			continue
		}
		type job struct {
			sp   tdelta.Spec
			b, t []byte
		}
		var jobs []job
		var total int64
		for k := range c[j].f.Tensors {
			t := &c[j].f.Tensors[k]
			bt, ok := c[i].f.Lookup(t.Name)
			if !ok || bt.DType != t.DType || bt.Size() != t.Size() {
				continue
			}
			jobs = append(jobs, job{tdelta.Spec{Shape: t.Shape, W: tdelta.ElemWidth(t)},
				c[i].data[c[i].f.DataStart+bt.Begin : c[i].f.DataStart+bt.End], c[j].data[c[j].f.DataStart+t.Begin : c[j].f.DataStart+t.End]})
			total += t.Size()
		}
		type codec struct {
			name string
			enc  func(j job) []byte
			dec  func(j job, p []byte) []byte
		}
		codecs := []codec{
			{"T0 plane-XOR+DEFLATE", func(j job) []byte { p, _ := tdelta.Encode(tdelta.Plane, j.b, j.t, j.sp.W); return p },
				func(j job, p []byte) []byte { o, _ := tdelta.Decode(tdelta.Plane, j.b, p, len(j.t), j.sp.W); return o }},
			{"T1 PXR delta", func(j job) []byte { p, _ := tdelta.EncodePXR(j.b, j.t, j.sp.W); return p },
				func(j job, p []byte) []byte { o, _ := tdelta.DecodePXR(j.b, p, len(j.t), j.sp.W); return o }},
			{"PXR standalone (no base)", func(j job) []byte { p, _ := tdelta.EncodePXR(nil, j.t, j.sp.W); return p },
				func(j job, p []byte) []byte { o, _ := tdelta.DecodePXR(nil, p, len(j.t), j.sp.W); return o }},
			{"hybrid tensor (admission)", func(j job) []byte {
				p, _, _ := tdelta.EncodeTensor(j.sp, j.b, j.t, tdelta.Options{MinReuse: minReuse})
				return p
			},
				func(j job, p []byte) []byte {
					m, pl, _, err := tdelta.ParseWire(p, len(j.t))
					if err != nil {
						return nil
					}
					o, _ := tdelta.DecodeTensor(j.sp, j.b, m, pl, len(j.t))
					return o
				}},
		}
		allOuts := make([][][]byte, len(codecs))
		for ci, cd := range codecs {
			var ms runtime.MemStats
			outs := make([][]byte, len(jobs))
			allOuts[ci] = outs
			runtime.GC()
			runtime.ReadMemStats(&ms)
			a0, n0 := ms.TotalAlloc, ms.Mallocs
			t0 := time.Now()
			var out int64
			for k, jb := range jobs {
				outs[k] = cd.enc(jb)
				out += int64(len(outs[k]))
			}
			enc := time.Since(t0)
			runtime.ReadMemStats(&ms)
			a1, n1 := ms.TotalAlloc, ms.Mallocs
			t1 := time.Now()
			for k, jb := range jobs {
				if o := cd.dec(jb, outs[k]); !bytes.Equal(o, jb.t) {
					fatal(fmt.Errorf("%s: roundtrip mismatch", cd.name))
				}
			}
			dec := time.Since(t1)
			runtime.ReadMemStats(&ms)
			mib := float64(total) / (1 << 20)
			fmt.Printf("| %s | %s -> %s | %s | %.1f | %s | %.0f | %.0f | %.1f | %.1f | %d | %d |\n", short(f), c[i].label, c[j].label, cd.name, mib, pct(out, total),
				mib/enc.Seconds(), mib/dec.Seconds(), float64(a1-a0)/(1<<20)/mib, float64(ms.TotalAlloc-a1)/(1<<20)/mib, n1-n0, ms.Mallocs-n1)
		}
		var win, winB, flWin, flWinB int64
		for k, jb := range jobs {
			if len(allOuts[1][k]) < len(allOuts[0][k]) {
				win++
				winB += int64(len(jb.t))
			} else {
				flWin++
				flWinB += int64(len(jb.t))
			}
		}
		fmt.Printf("\n%s %s -> %s: PXR delta smaller than DEFLATE plane-XOR on %d of %d tensors (%s %% of tensor bytes); DEFLATE smaller on %d (%s %%).\n\n", short(f), c[i].label, c[j].label, win, len(jobs), pct(winB, total), flWin, pct(flWinB, total))
	}
}
