package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"zeros3-z3/dagz3"
	"zeros3-z3/repz3"
)

var caps = []int{0, 1, 2, 3, 4}

type workload struct {
	name     string
	versions [][]byte
}

// econTable prints cumulative persisted bytes (representations + components + tables + indexes)
// of each fixed policy after the first, the second and the last version.
func econTable(w workload) []dagz3.Curve {
	curves, err := dagz3.LevelCurve(w.versions, caps)
	must(err)
	cum := func(c *dagz3.Curve, n int, per int64) (t int64) {
		for i := 0; i < n && i < len(c.Bytes); i++ {
			t += c.Bytes[i] + per*int64(c.Objects[i])
		}
		return
	}
	for _, per := range []int64{0, 64} {
		for _, n := range uniq([]int{1, 2, len(w.versions)}) {
			var row []string
			bestK, best := 0, int64(1)<<62
			for i := range curves {
				v := cum(&curves[i], n, per)
				row = append(row, fmt.Sprintf("%8d", v))
				if v < best {
					best, bestK = v, curves[i].Cap
				}
			}
			win := fmt.Sprintf("d%d", bestK)
			if bestK == 0 {
				win = "CDC"
			}
			fmt.Printf("%-34s ovh %3d  after %d  %s   %s\n", w.name, per, n, strings.Join(row, " "), win)
		}
	}
	return curves
}

func uniq(a []int) (o []int) {
	seen := map[int]bool{}
	for _, v := range a {
		if !seen[v] {
			seen[v] = true
			o = append(o, v)
		}
	}
	return
}

func workloads() []workload {
	var wl []workload
	var a, b [][]byte
	for r := 0; r < 6; r++ {
		a = append(a, dagz3.ChainA(r, 160, 6))
	}
	for r := 0; r < 4; r++ {
		b = append(b, dagz3.ChainB(r))
	}
	wl = append(wl, workload{"A zstd>tar>gzip>sqlite (6 revs)", a}, workload{"B gzip>tar>zstd>car (4 revs)", b})

	// complementary: compressed archive -> database, compressed archive -> CAR (single-level outers)
	var gdb, gcar [][]byte
	for r := 0; r < 5; r++ {
		pg := dagz3.DB(28, 160)
		for k := 1; k <= r; k++ {
			pg = dagz3.Mutate(pg, uint64(2*k+101), 6)
		}
		gdb = append(gdb, dagz3.Gzip(dagz3.Tar([]string{"data/app.db"}, map[string][]byte{"data/app.db": dagz3.Join(pg)}), 6))
	}
	for r := 0; r < 4; r++ {
		gcar = append(gcar, dagz3.Gzip(dagz3.Tar([]string{"export/blocks.car"}, map[string][]byte{"export/blocks.car": dagz3.CARAdd(r)}), 6))
	}
	wl = append(wl, workload{"complementary tar.gz>sqlite (5)", gdb}, workload{"complementary tar.gz>car (4)", gcar})
	var zdb [][]byte
	for r := 0; r < 4; r++ {
		pg := dagz3.DB(28, 160)
		for k := 1; k <= r; k++ {
			pg = dagz3.Mutate(pg, uint64(2*k+101), 6)
		}
		zdb = append(zdb, dagz3.Zstd(dagz3.Tar([]string{"data/app.db"}, map[string][]byte{"data/app.db": dagz3.Join(pg)})))
	}
	wl = append(wl, workload{"Z3-25 zstd>tar>sqlite (4)", zdb})

	// destructive overlap: random (incompressible) content in a gzip: exact replay exposes nothing
	// the compressed bytes' own chunks do not already share
	var ov [][]byte
	base := dagz3.Random(7, 600<<10)
	for r := 0; r < 4; r++ {
		ov = append(ov, dagz3.Gzip(append(append([]byte(nil), base...), dagz3.Random(uint64(2*r+11), 2000)...), 6))
	}
	wl = append(wl, workload{"overlap: gzip of random, appended", ov})
	// destructive overlap: an uncompressed tar holding two identical databases. Chunk-level CDC,
	// the tar split and the SQLite page tree all capture the duplicate; nesting adds only metadata.
	var dup [][]byte
	for r := 0; r < 3; r++ {
		pg := dagz3.DB(28, 96)
		for k := 1; k <= r; k++ {
			pg = dagz3.Mutate(pg, uint64(2*k+101), 4)
		}
		d := dagz3.Join(pg)
		dup = append(dup, dagz3.Tar([]string{"a.db", "b.db"}, map[string][]byte{"a.db": d, "b.db": d}))
	}
	wl = append(wl, workload{"overlap: tar of duplicate dbs (3)", dup})
	// negative control: format matches, no reuse (unrelated databases in tar.gz)
	var neg [][]byte
	for r := 0; r < 3; r++ {
		neg = append(neg, dagz3.Gzip(dagz3.Tar([]string{"data/app.db"}, map[string][]byte{"data/app.db": dagz3.Join(dagz3.DB(uint64(2*r+301), 96))}), 6))
	}
	wl = append(wl, workload{"negative control: unrelated dbs", neg})

	// cross-format revision chain: rev 0 in tar.gz, rev 1 in tar.zst, rev 2 loose
	pg0 := dagz3.DB(28, 160)
	pg1 := dagz3.Mutate(pg0, 103, 6)
	pg2 := dagz3.Mutate(pg1, 105, 6)
	t := func(pg [][]byte) []byte {
		return dagz3.Tar([]string{"data/app.db"}, map[string][]byte{"data/app.db": dagz3.Join(pg)})
	}
	wl = append(wl, workload{"cross-format gz > zst > loose", [][]byte{dagz3.Gzip(t(pg0), 6), dagz3.Zstd(t(pg1)), dagz3.Join(pg2)}})
	// compression-level change: the same tar plaintext as gzip -1, gzip -9, then zstd
	tp := t(pg0)
	wl = append(wl, workload{"recompress gz1 > gz9 > zstd", [][]byte{dagz3.Gzip(tp, 1), dagz3.Gzip(tp, 9), dagz3.Zstd(tp)}})
	// the same semantic payload under different outer formats at once
	wl = append(wl, workload{"same db: tar.gz + tar.zst", [][]byte{dagz3.Gzip(tp, 6), dagz3.Zstd(tp)}})

	return wl
}

func econSection() {
	fmt.Printf("%-34s %-8s %-8s %8s %8s %8s %8s %8s   winner (cumulative persisted bytes)\n", "workload", "overhead", "versions", "CDC", "d1", "d2", "d3", "d4")
	wl := workloads()
	for _, w := range wl {
		econTable(w)
		fmt.Println()
	}
	existingCAS()
	realZstd()
	_ = bytes.Equal
	_ = repz3.Version
}

// existingCAS: the store already holds the database as a loose SQLite representation; the
// archive that contains it arrives afterwards. Analysis is deterministic, so the child
// representation it produces is the object already stored.
func existingCAS() {
	db := dagz3.Join(dagz3.DB(28, 160))
	tp := dagz3.Tar([]string{"data/app.db"}, map[string][]byte{"data/app.db": db})
	arch := dagz3.Gzip(tp, 6)
	loose, err := dagz3.Discover(db, dagz3.DefaultBudget(1))
	must(err)
	for _, k := range []int{0, 4} {
		st := repz3.NewMemStore()
		_, _, err := loose.Persist(st)
		must(err)
		before := st.Len()
		b, err := dagz3.Discover(arch, dagz3.DefaultBudget(k))
		must(err)
		_, added, err := b.Persist(st)
		must(err)
		var nb int64
		for _, id := range added {
			n, _ := st.Size(id)
			nb += n
		}
		fmt.Printf("existing-CAS: store holds loose sqlite (%d objs); archive at cap %d adds %d objects, %d B (archive %d B)\n", before, k, len(added), nb, len(arch))
	}
}

// realZstd repeats the depth-4 chain A economics with a real libzstd 1.5.7 producer when
// Z3_PYZSTD names a directory holding the python "zstandard" wheel (an optional oracle; the
// committed tests never need it). The laboratory synthesizer's streams are not model-coded, so
// this checks that the zstd-bearing conclusions do not depend on the synthetic producer.
func realZstd() {
	dir := os.Getenv("Z3_PYZSTD")
	if dir == "" {
		fmt.Println("real-producer check: skipped (set Z3_PYZSTD to a directory with the python zstandard package)")
		return
	}
	var vs [][]byte
	for r := 0; r < 4; r++ {
		pg := dagz3.DB(28, 160)
		for k := 1; k <= r; k++ {
			pg = dagz3.Mutate(pg, uint64(2*k+101), 6)
		}
		tarb := dagz3.Tar([]string{"etc/app.conf", "data/app.db.gz", "README"}, map[string][]byte{
			"etc/app.conf": []byte("mode=prod\nretries=3\n"), "data/app.db.gz": dagz3.Gzip(dagz3.Join(pg), 6), "README": bytes.Repeat([]byte("keep this file\n"), 40)})
		cmd := exec.Command("python3", "-I", "-c", "import sys; sys.path.insert(0, sys.argv[1]); import zstandard; sys.stdout.buffer.write(zstandard.ZstdCompressor(level=3, threads=0).compress(sys.stdin.buffer.read()))", dir)
		cmd.Stdin = bytes.NewReader(tarb)
		out, err := cmd.Output()
		must(err)
		vs = append(vs, out)
	}
	fmt.Println("real-producer check (libzstd 1.5.7 -3):")
	econTable(workload{"A with real zstd (4 revs)", vs})
}
