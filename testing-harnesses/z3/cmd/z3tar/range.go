package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"sort"

	"zeros3-z3/deflate"
	"zeros3-z3/ir"
	"zeros3-z3/lab"
	"zeros3-z3/tarz3"
)

type piece struct{ off, n int64 }

// layouts returns the plaintext piece boundaries of the three segmentations
// (the same cuts lab.tarPieces makes): CDC chunks, tarmem (header piece +
// whole payload piece per member), tarmem-cdc64k (payloads >= 64 KiB cut by CDC).
func layouts(r *rev) map[string][]piece {
	out := map[string][]piece{}
	lab.ChunkCDC(bytes.NewReader(r.plain), func(off int64, c []byte) error {
		out["cdc"] = append(out["cdc"], piece{off, int64(len(c))})
		return nil
	})
	for _, name := range []string{"tarmem", "tarmem-cdc64k"} {
		var ps []piece
		for i := range r.ar.Members {
			m := &r.ar.Members[i]
			if m.PayLen == 0 {
				ps = append(ps, piece{m.Off, int64(m.HdrLen)})
				continue
			}
			if m.PayLen <= 512 { // small members travel with their header
				ps = append(ps, piece{m.Off, m.End() - m.Off})
				continue
			}
			ps = append(ps, piece{m.Off, int64(m.HdrLen)})
			if name == "tarmem-cdc64k" && m.PayLen >= 64<<10 {
				lab.ChunkCDC(bytes.NewReader(r.plain[m.PayOff:m.End()]), func(off int64, c []byte) error {
					ps = append(ps, piece{m.PayOff + off, int64(len(c))})
					return nil
				})
				continue
			}
			ps = append(ps, piece{m.PayOff, m.End() - m.PayOff})
		}
		if r.ar.TrailOff < r.ar.Size {
			ps = append(ps, piece{r.ar.TrailOff, r.ar.Size - r.ar.TrailOff})
		}
		out[name] = ps
	}
	return out
}

// touch returns the count and bytes of pieces overlapping any span.
func touch(ps []piece, spans [][2]int64) (n int, bytes int64) {
	seen := map[int]bool{}
	for _, s := range spans {
		k := sort.Search(len(ps), func(i int) bool { return ps[i].off+ps[i].n > s[0] })
		for ; k < len(ps) && ps[k].off < s[1]; k++ {
			if !seen[k] {
				seen[k] = true
				n++
				bytes += ps[k].n
			}
		}
	}
	return
}

func indexBytes(ps []piece) int {
	var b []byte
	for _, p := range ps {
		b = binary.AppendUvarint(b, uint64(p.n)) // offsets are implied by the running sum of lengths
	}
	return len(b)
}

var layoutNames = []string{"cdc", "tarmem", "tarmem-cdc64k"}

type memSink struct {
	plain bytes.Buffer
	units [][]byte
}

func (m *memSink) Plain(p []byte) error { m.plain.Write(p); return nil }
func (m *memSink) Unit(i int, d []byte) error {
	m.units = append(m.units, append([]byte(nil), d...))
	return nil
}

func runRange(revs map[string][]*rev, order []string, trials int) {
	r := rand.New(rand.NewSource(5))
	fmt.Println("Plaintext piece layouts of the last revision of each family. index = piece-length varints (offsets are their prefix sums); member index = one varint offset + path per member.")
	fmt.Println()
	fmt.Println("| family | tar MiB | members | layout | pieces | piece index B | % of tar | one member: pieces / bytes fetched / member bytes | 4 KiB range: pieces / fetched | 64 KiB range: pieces / fetched | 1 MiB range: pieces / fetched | locate a member without an index: header pieces read (avg) |")
	fmt.Println("|---|---:|---:|---|---:|---:|---:|---|---|---|---|---:|")
	for _, f := range order {
		list := revs[f]
		cur := list[len(list)-1]
		if cur.ar == nil {
			continue
		}
		var pay []int
		for i := range cur.ar.Members {
			if cur.ar.Members[i].PayLen > 0 {
				pay = append(pay, i)
			}
		}
		if len(pay) == 0 {
			continue
		}
		ls := layouts(cur)
		for _, ln := range layoutNames {
			ps := ls[ln]
			var mp, mb, mm int64
			for t := 0; t < trials; t++ {
				m := &cur.ar.Members[pay[r.Intn(len(pay))]]
				n, b := touch(ps, [][2]int64{{m.Off, m.PayOff + m.PayLen}})
				mp += int64(n)
				mb += b
				mm += m.PayLen + int64(m.HdrLen)
			}
			row := fmt.Sprintf("| %s | %.1f | %d | %s | %d | %d | %s | %.1f / %.1f KiB / %.1f KiB |", short(f), float64(len(cur.plain))/(1<<20), len(cur.ar.Members), ln, len(ps),
				indexBytes(ps), pct(int64(indexBytes(ps)), int64(len(cur.plain))), float64(mp)/float64(trials), float64(mb)/float64(trials)/1024, float64(mm)/float64(trials)/1024)
			for _, sz := range []int64{4 << 10, 64 << 10, 1 << 20} {
				var np, nb int64
				for t := 0; t < trials; t++ {
					if sz >= int64(len(cur.plain)) {
						continue
					}
					a := r.Int63n(int64(len(cur.plain)) - sz)
					n, b := touch(ps, [][2]int64{{a, a + sz}})
					np += int64(n)
					nb += b
				}
				row += fmt.Sprintf(" %.1f / %.1f KiB |", float64(np)/float64(trials), float64(nb)/float64(trials)/1024)
			}
			// without an index the walker reads header pieces one after the other until the member
			var scan float64
			if ln != "cdc" {
				scan = float64(len(cur.ar.Members)) / 2
			}
			if ln == "cdc" {
				row += " n/a (headers are not addressable) |"
			} else {
				row += fmt.Sprintf(" %.0f |", scan)
			}
			fmt.Println(row)
		}
		var pathB int
		for i := range cur.ar.Members {
			pathB += len(cur.ar.Members[i].Path) + 4
		}
		fmt.Printf("| %s | member index (offset + path) | | | %d | %d | %s | | | | | |\n", short(f), len(cur.ar.Members), pathB, pct(int64(pathB), int64(len(cur.plain))))
	}

	fmt.Println("\nCompressed-range reads of gzip layers through the DEFLATE IR range index (every unit indexed, 16 KiB units): the plaintext spans MaterializeRange asks for, mapped to pieces of each layout.")
	fmt.Println()
	fmt.Println("| family | compressed range | units decoded (avg) | plaintext spans fetched KiB (avg) | CDC pieces / KiB | tarmem pieces / KiB | tarmem-cdc64k pieces / KiB | exact |")
	fmt.Println("|---|---|---:|---:|---|---|---|---|")
	for _, f := range order {
		list := revs[f]
		cur := list[len(list)-1]
		if !cur.gz || cur.ar == nil || len(cur.raw) < 2<<20 {
			continue
		}
		br := bytes.NewReader(cur.raw)
		hdr, err := deflate.GzipHeader(br)
		if err != nil {
			continue
		}
		var sink memSink
		ss, _, err := ir.EncodeStream(br, ir.StreamConfig{Target: 16 << 10, Profile: ir.AutoProfile}, &sink)
		if err != nil {
			fmt.Printf("| %s | IR encode failed: %v |||||||\n", short(f), err)
			continue
		}
		raw := cur.raw[len(hdr) : len(hdr)+int(ss.StreamLen)]
		ix, err := ss.Index()
		if err != nil {
			continue
		}
		plain := sink.plain.Bytes()
		ls := layouts(cur)
		unit := func(i int) ([]byte, error) { return sink.units[i], nil }
		for _, sz := range []int64{4 << 10, 64 << 10, 1 << 20} {
			var units int
			var spanB int64
			var np, nb [3]int64
			ok := true
			for t := 0; t < trials; t++ {
				a := r.Int63n(int64(len(raw)) - sz)
				var spans [][2]int64
				plainAt := func(from, to int64) ([]byte, error) {
					spans = append(spans, [2]int64{from, to})
					return plain[from:to], nil
				}
				got, need, err := ir.MaterializeRange(ss.Skeleton, ix, 1, unit, plainAt, a, a+sz)
				if err != nil || !bytes.Equal(got, raw[a:a+sz]) {
					ok = false
					continue
				}
				units += need.UnitsDecoded
				spanB += need.PlainBytes
				for k, ln := range layoutNames {
					n, b := touch(ls[ln], spans)
					np[k] += int64(n)
					nb[k] += b
				}
			}
			tn := float64(trials)
			fmt.Printf("| %s | %d KiB | %.1f | %.1f | %.1f / %.1f | %.1f / %.1f | %.1f / %.1f | %v |\n", short(f), sz>>10, float64(units)/tn, float64(spanB)/tn/1024,
				float64(np[0])/tn, float64(nb[0])/tn/1024, float64(np[1])/tn, float64(nb[1])/tn/1024, float64(np[2])/tn, float64(nb[2])/tn/1024, ok)
		}
	}
	_ = tarz3.BlockSize
}
