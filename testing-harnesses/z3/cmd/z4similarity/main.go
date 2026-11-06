// z4similarity measures research deltas without modifying production storage.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"

	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"

	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"
	"zeros3-z3/lab"
	"zeros3-z3/pdelta"
	"zeros3-z3/simdelta"
)

type Family struct {
	Name, Kind string
	Paths      []string
}
type Input struct {
	Path, SHA256 string
	Bytes        int
}
type Row struct {
	Family, Kind, Mode                                                                       string
	Inputs                                                                                   []Input
	Revisions, Chunks, Unique, Deltas, Multi, MaxDepth                                       int
	Raw, CDC, FineCDC, Standalone, IndexBytes, Authoritative, Additive, Migration, RootBytes int64
	Algorithms                                                                               map[string]int64
	Wins                                                                                     map[string]int
	Discovery                                                                                map[string]int
	EncodeMS, DecodeMS, QueryMS                                                              float64
	Heap                                                                                     uint64
	Ranges                                                                                   map[string]float64
	Archive                                                                                  string
}
type object struct {
	Data, Wire []byte
	Depth      int
	Features   []uint64
	Offset     int
}
type manifest struct {
	Roots  [][][32]byte
	Hashes [][32]byte
	Sizes  []int
}

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func emit(v any) { must(json.NewEncoder(os.Stdout).Encode(v)) }
func main() {
	manifestPath := flag.String("manifest", "", "JSON corpus manifest")
	out := flag.String("out", "", "exact replay store directory")
	scale := flag.Int("scale", 0, "synthetic indexed chunks (four features each)")
	replay := flag.String("replay", "", "replay exported family directory")
	mode := flag.String("mode", "cdc", "cdc or local-4k")
	flag.Parse()
	if *scale > 0 {
		indexScale(*scale)
		return
	}
	if *replay != "" {
		replayStore(*replay)
		return
	}
	var families []Family
	b, e := os.ReadFile(*manifestPath)
	must(e)
	must(json.Unmarshal(b, &families))
	must(os.MkdirAll(*out, 0755))
	for _, f := range families {
		run(f, *out, *mode)
	}
}
func run(f Family, dir, mode string) {
	r := Row{Family: f.Name, Kind: f.Kind, Mode: mode, Algorithms: map[string]int64{}, Wins: map[string]int{}, Discovery: map[string]int{}, Ranges: map[string]float64{}}
	var objs []object
	ids := map[[32]byte]int{}
	cdcSeen, fineSeen := map[[32]byte]bool{}, map[[32]byte]bool{}
	indexes := []*simdelta.Index{simdelta.NewIndex(), simdelta.NewIndex(), simdelta.NewIndex(), simdelta.NewIndex()}
	names := []string{"bottom16", "super4", "edges2", "local64"}
	var m manifest
	var depthRead int64
	start := time.Now()
	for _, path := range f.Paths {
		data, e := os.ReadFile(path)
		must(e)
		sum := sha256.Sum256(data)
		r.Inputs = append(r.Inputs, Input{path, hex.EncodeToString(sum[:]), len(data)})
		r.Raw += int64(len(data))
		r.Revisions++
		cdc := lab.ChunkScaled(data, 16<<10, 64<<10, 256<<10)
		for _, c := range cdc {
			h := sha256.Sum256(c)
			r.CDC += 36
			if !cdcSeen[h] {
				r.CDC += lab.PackedLen(c) + 96
				cdcSeen[h] = true
			}
		}
		fine := lab.ChunkScaled(data, 2<<10, 8<<10, 32<<10)
		for _, c := range fine {
			h := sha256.Sum256(c)
			r.FineCDC += 36
			if !fineSeen[h] {
				r.FineCDC += lab.PackedLen(c) + 96
				fineSeen[h] = true
			}
		}
		chunks := cdc
		if mode == "local-4k" {
			chunks = lab.ChunkScaled(data, 1024, 4096, 16<<10)
		}
		var roots [][32]byte
		for _, target := range chunks {
			r.Chunks++
			r.RootBytes += 36
			h := sha256.Sum256(target)
			roots = append(roots, h)
			if _, ok := ids[h]; ok {
				continue
			}
			features := simdelta.Features(target, 16)
			sets := [][]uint64{features, simdelta.Superfeatures(features), simdelta.Edges(target), simdelta.BlockFeatures(target)}
			candidates := map[int]int{}
			qtime := time.Now()
			for i, x := range indexes {
				q := x.Query(sets[i], 4)
				r.Discovery[names[i]+"_queries"]++
				if len(q) > 0 {
					r.Discovery[names[i]+"_hits"]++
				}
				for _, c := range q {
					candidates[int(c.ID)] += c.Hits
				}
			}
			r.QueryMS += float64(time.Since(qtime).Microseconds()) / 1000
			ranked := make([]int, 0, len(candidates))
			for id := range candidates {
				if objs[id].Depth < 2 {
					ranked = append(ranked, id)
				}
			}
			sort.Slice(ranked, func(i, j int) bool {
				if candidates[ranked[i]] != candidates[ranked[j]] {
					return candidates[ranked[i]] > candidates[ranked[j]]
				}
				return ranked[i] > ranked[j]
			})
			if len(ranked) > 8 {
				ranked = ranked[:8]
			}
			direct, e := simdelta.Encode("direct", nil, target)
			must(e)
			best := direct
			bestDepth := 0
			winner := "direct"
			var top []int
			type scored struct{ id, n int }
			var scores []scored
			for _, id := range ranked {
				w, e := simdelta.Encode("adaptive", [][]byte{objs[id].Data}, target)
				must(e)
				scores = append(scores, scored{id, len(w)})
				if len(w) < len(best) {
					best = w
					bestDepth = objs[id].Depth + 1
					winner = "adaptive"
				}
			}
			sort.Slice(scores, func(i, j int) bool {
				if scores[i].n != scores[j].n {
					return scores[i].n < scores[j].n
				}
				return scores[i].id < scores[j].id
			})
			for _, s := range scores {
				top = append(top, s.id)
			}
			for _, a := range simdelta.Algorithms {
				w := direct
				if a != "direct" && len(top) > 0 {
					w, e = simdelta.Encode(a, [][]byte{objs[top[0]].Data}, target)
					must(e)
				}
				r.Algorithms[a] += int64(len(w) + 96)
				if len(w) < len(best) {
					best = w
					bestDepth = objs[top[0]].Depth + 1
					winner = a
				}
			}
			if len(top) > 0 {
				r.Algorithms["pdelta-estimate"] += int64(pdelta.Cost(objs[top[0]].Data, target).Cost + 74 + 96)
			} else {
				r.Algorithms["pdelta-estimate"] += int64(len(direct) + 96)
			}
			for _, pool := range []int{2, 4} {
				if len(top) < pool {
					r.Algorithms[fmt.Sprintf("multi%d", pool)] += int64(len(direct) + 96)
					continue
				}
				var bases [][]byte
				d := 0
				for _, id := range top[:pool] {
					bases = append(bases, objs[id].Data)
					if objs[id].Depth > d {
						d = objs[id].Depth
					}
				}
				w, e := simdelta.Encode("adaptive", bases, target)
				must(e)
				r.Algorithms[fmt.Sprintf("multi%d", pool)] += int64(len(w) + 96)
				if len(w) < len(best) {
					best = w
					bestDepth = d + 1
					winner = fmt.Sprintf("multi%d", pool)
				}
			}
			hd, e := simdelta.Inspect(best)
			must(e)
			if len(hd.Bases) > 0 {
				r.Deltas++
			}
			if len(hd.Bases) > 1 {
				r.Multi++
			}
			r.Wins[winner]++
			r.Standalone += int64(len(best) + 96)
			r.IndexBytes += 32
			for _, set := range sets {
				r.IndexBytes += int64(len(set) * 12)
			}
			if bestDepth > r.MaxDepth {
				r.MaxDepth = bestDepth
			}
			id := len(objs)
			ids[h] = id
			objs = append(objs, object{append([]byte{}, target...), best, bestDepth, features, 0})
			for i, x := range indexes {
				x.Add(uint32(id), sets[i])
			}
		}
		m.Roots = append(m.Roots, roots)
		m.Hashes = append(m.Hashes, sum)
		m.Sizes = append(m.Sizes, len(data))
	}
	r.EncodeMS = float64(time.Since(start).Microseconds()) / 1000
	r.Unique = len(objs)
	r.Standalone += r.RootBytes
	r.Authoritative = r.Standalone + r.IndexBytes
	r.Additive = r.CDC + r.Authoritative
	r.Migration = r.Additive
	storeDir := filepath.Join(dir, f.Name+"-"+mode)
	must(os.MkdirAll(storeDir, 0755))
	for h, id := range ids {
		must(os.WriteFile(filepath.Join(storeDir, hex.EncodeToString(h[:])+".delta"), objs[id].Wire, 0644))
	}
	mb, e := json.Marshal(m)
	must(e)
	must(os.WriteFile(filepath.Join(storeDir, "roots.json"), mb, 0644))
	r.Archive = storeDir
	// Cold decoding: no decoded-object cache. Measure closure bytes repeatedly.
	get := func(h [32]byte) ([]byte, error) {
		id, ok := ids[h]
		if !ok {
			return nil, fmt.Errorf("missing")
		}
		depthRead += int64(len(objs[id].Wire))
		return objs[id].Wire, nil
	}
	t := time.Now()
	for rev, root := range m.Roots {
		hs := sha256.New()
		n := 0
		for _, h := range root {
			b, e := simdelta.Resolve(h, get, 4, int64(simdelta.MaxBytes)*64)
			must(e)
			n += len(b)
			_, _ = hs.Write(b)
		}
		if n != m.Sizes[rev] || !bytes.Equal(hs.Sum(nil), m.Hashes[rev][:]) {
			panic("replay mismatch")
		}
	}
	r.DecodeMS = float64(time.Since(t).Microseconds()) / 1000
	if r.Raw > 0 {
		r.Ranges["whole_wire_amplification"] = float64(depthRead) / float64(r.Raw)
	}
	for _, n := range []int{4096, 65536, 1048576} {
		var fetched, logical int64
		for _, roots := range m.Roots {
			left := n
			for _, h := range roots {
				if left <= 0 {
					break
				}
				before := depthRead
				b, e := simdelta.Resolve(h, get, 4, int64(simdelta.MaxBytes)*64)
				must(e)
				take := len(b)
				if take > left {
					take = left
				}
				left -= take
				logical += int64(take)
				fetched += depthRead - before
			}
		}
		if logical > 0 {
			r.Ranges[fmt.Sprint(n)] = float64(fetched) / float64(logical)
		}
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	r.Heap = mem.HeapAlloc
	emit(r)
}
func replayStore(dir string) {
	b, e := os.ReadFile(filepath.Join(dir, "roots.json"))
	must(e)
	var m manifest
	must(json.Unmarshal(b, &m))
	start := time.Now()
	reads, physical := 0, int64(0)
	get := func(h [32]byte) ([]byte, error) {
		reads++
		b, e := os.ReadFile(filepath.Join(dir, hex.EncodeToString(h[:])+".delta"))
		physical += int64(len(b))
		return b, e
	}
	for i, root := range m.Roots {
		hs := sha256.New()
		n := 0
		for _, h := range root {
			b, e := simdelta.Resolve(h, get, 4, int64(simdelta.MaxBytes)*64)
			must(e)
			hs.Write(b)
			n += len(b)
		}
		if n != m.Sizes[i] || !bytes.Equal(hs.Sum(nil), m.Hashes[i][:]) {
			panic("independent replay mismatch")
		}
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	emit(map[string]any{"replay": dir, "revisions": len(m.Roots), "reads": reads, "wire_bytes": physical, "ms": float64(time.Since(start).Microseconds()) / 1000, "heap": mem.HeapAlloc})
}
func indexScale(n int) {
	if n <= 0 || n > 10000000 {
		panic("scale bound")
	}
	p := make([]simdelta.Posting, n*4)
	rng := rand.New(rand.NewSource(811))
	t := time.Now()
	for i := range p {
		p[i] = simdelta.Posting{Feature: rng.Uint64(), ID: uint32(i / 4)}
	}
	simdelta.Sort(p)
	build := time.Since(t)
	f, e := os.CreateTemp("", "b1-index-*.table")
	must(e)
	defer os.Remove(f.Name())
	defer f.Close()
	bw := bufio.NewWriterSize(f, 1<<20)
	t = time.Now()
	must(simdelta.WriteTable(bw, p))
	must(bw.Flush())
	must(f.Sync())
	write := time.Since(t)
	t = time.Now()
	for i := 0; i < 1000; i++ {
		v := p[rng.Intn(len(p))]
		ids, e := simdelta.DiskLookup(f, int64(len(p)), v.Feature)
		must(e)
		if len(ids) != 1 || ids[0] != v.ID {
			panic("index lookup")
		}
	}
	q := time.Since(t)
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	emit(map[string]any{"chunks": n, "features_per_chunk": 4, "resident_array_bytes": len(p) * 16, "disk_bytes": len(p) * 12, "build_ms": float64(build.Microseconds()) / 1000, "write_ms": float64(write.Microseconds()) / 1000, "query_us": float64(q.Microseconds()) / 1000, "heap": m.HeapAlloc})
}
