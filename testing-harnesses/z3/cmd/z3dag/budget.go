package main

import (
	"fmt"
	"time"

	"zeros3-z3/dagz3"
)

func budgetSection() {
	fmt.Printf("%-44s %-8s %7s %10s %10s  %-28s %-18s %s\n", "hostile input", "result", "attempt", "inspected", "decoded", "stops", "accepted", "time")
	run := func(name string, data []byte, mut func(b *dagz3.Budget)) {
		b := dagz3.DefaultBudget(4)
		if mut != nil {
			mut(b)
		}
		t := time.Now()
		root, err := dagz3.Discover(data, b)
		must(err)
		kind := fmt.Sprintf("depth %d", dagz3.Depth(root))
		if dagz3.IsPlainCDC(root) {
			kind = "stored"
		}
		fmt.Printf("%-44s %-8s %7d %10d %10d  %-28s %-18s %v\n", name, kind, b.Attempts, b.Inspected, b.Decoded, fmtMap(b.Stops), fmtMap(b.Accepted), time.Since(t).Round(time.Millisecond))
	}

	// many tiny magic-looking members
	names := make([]string, 0, 5000)
	files := map[string][]byte{}
	for i := 0; i < 5000; i++ {
		n := fmt.Sprintf("m%05d", i)
		names = append(names, n)
		f := make([]byte, 600)
		copy(f, "SQLite format 3\x00")
		f[16], f[17] = 0, 7 // fails to parse
		f[100], f[101] = byte(i), byte(i>>8)
		files[n] = f
	}
	run("5000 tiny SQLite-magic members (unparseable)", dagz3.Tar(names, files), nil)
	// 5000 gzip-magic members
	for _, n := range names {
		f := make([]byte, 600)
		copy(f, []byte{0x1f, 0x8b, 8})
		f[100], f[101] = byte(len(files)), byte(len(n))
		f[200] = n[3]
		f[201] = n[4]
		f[202] = n[5]
		f[203] = n[2]
		files[n] = f
	}
	run("5000 tiny gzip-magic members (invalid)", dagz3.Tar(names, files), nil)

	// a decompression bomb: 64 MiB of zeros in 64 KiB gzip
	bomb := dagz3.Gzip(make([]byte, 64<<20), 9)
	run(fmt.Sprintf("gzip bomb %d B -> 64 MiB, MaxDecoded 1 MiB", len(bomb)), bomb, func(b *dagz3.Budget) { b.MaxDecoded = 1 << 20 })
	run(fmt.Sprintf("same bomb, default MaxDecoded (256 MiB)"), bomb, nil)

	// cumulative: eight members, each fine alone, together over the budget
	pat := dagz3.Random(5, 700)
	var mem []string
	mf := map[string][]byte{}
	for i := 0; i < 8; i++ {
		n := fmt.Sprintf("g%d.gz", i)
		mem = append(mem, n)
		body := make([]byte, 0, 420<<10)
		for len(body) < 400<<10 {
			body = append(body, pat...)
			body = append(body, byte(i), byte(len(body)))
		}
		mf[n] = dagz3.Gzip(body, 6)
	}
	run("8 gzip members x 400 KiB, MaxDecoded 1 MiB", dagz3.Tar(mem, mf), func(b *dagz3.Budget) { b.MaxDecoded = 1 << 20 })
	run("8 gzip members x 400 KiB, MaxDecoded 64 MiB", dagz3.Tar(mem, mf), func(b *dagz3.Budget) { b.MaxDecoded = 64 << 20 })

	// nested containers deeper than the hard maximum
	deep := dagz3.Join(dagz3.DB(61, 16))
	for i := 0; i < 5; i++ {
		deep = dagz3.Gzip(dagz3.Tar([]string{"x"}, map[string][]byte{"x": deep}), 6)
	}
	run("gzip>tar nested 5 times around a database", deep, nil)

	// attempts budget
	many := map[string][]byte{}
	var mn []string
	for i := 0; i < 400; i++ {
		n := fmt.Sprintf("d%03d.db", i)
		mn = append(mn, n)
		many[n] = dagz3.Join(dagz3.DB(uint64(2*i+1001), 6))
	}
	run("400 small valid databases, MaxChildren 64", dagz3.Tar(mn, many), nil)
	run("400 small valid databases, MaxAttempts 20", dagz3.Tar(mn, many), func(b *dagz3.Budget) { b.MaxChildren = 1000; b.MaxAttempts = 20 })
}

func fmtMap(m map[string]int) string {
	s := ""
	for _, k := range dagz3.SortedKeys(m) {
		s += fmt.Sprintf("%s=%d ", k, m[k])
	}
	if s == "" {
		return "-"
	}
	return s[:len(s)-1]
}
