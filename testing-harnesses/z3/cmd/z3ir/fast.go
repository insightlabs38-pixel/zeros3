package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"zeros3-z3/deflate"
	"zeros3-z3/ir"
)

// fastCmd: Z3-08C reset-penalty study on zlib/gzip fast levels. A plaintext
// file is compressed by python zlib (memLevel 8) and GNU gzip at levels 1-3
// (and 6 as a control); every stream is encoded predictively whole-stream and
// as 16 KiB / 4 KiB units with each fast-mode profile, and with exact-emulation
// warm-up windows of different sizes.
func fastCmd(args []string) {
	fs := flag.NewFlagSet("fast", flag.ExitOnError)
	plainF := fs.String("plain", "", "plaintext file (gz/tar inputs are used as is)")
	mib := fs.Int("mib", 8, "bytes of the plaintext to use (MiB)")
	py := fs.String("py", "python3", "python with zlib")
	prods := fs.String("producers", "zlib1,zlib2,zlib3,zlib6,gzip1,gzip2,gzip3,gzip6", "producers")
	allProf := fs.Bool("all-profiles", false, "also try the emulators of the other levels")
	fs.Parse(args)
	pb, err := os.ReadFile(*plainF)
	if err != nil {
		fatal(err)
	}
	if len(pb) > *mib<<20 {
		pb = pb[:*mib<<20]
	}
	pol16 := ir.Policy{Kind: ir.PTokRun, Target: 16 << 10}
	pol4 := ir.Policy{Kind: ir.PTokRun, Target: 4 << 10}
	fmt.Printf("plaintext %s: %d bytes\n\n", *plainF, len(pb))
	fmt.Println("| producer | comp bytes | profile | mono % | tok16K % (reset pp) | tok4K % | enc MiB/s (tok16K) |")
	fmt.Println("|---|---:|---|---:|---:|---:|---:|")
	for _, pr := range split(*prods) {
		comp, err := compressWith(*py, pr, pb)
		if err != nil {
			fmt.Printf("| %s | n/a (%v) |||||\n", pr, err)
			continue
		}
		raw := comp
		if strings.HasPrefix(pr, "gzip") {
			_, raw2, err := parseGz(comp)
			if err != nil {
				fatal(err)
			}
			raw = raw2
		}
		s, err := deflate.Parse(raw, deflate.Options{})
		if err != nil {
			fatal(err)
		}
		type v struct {
			name string
			prof uint8
			warm int
		}
		vs := []v{{"P3 zlib-fast-like", 3, 0}, {"P0 generic", 0, 0}, {"P1 zlib-like", 1, 0}}
		for p := uint8(4); p <= 6; p++ {
			if !*allProf && !strings.HasSuffix(pr, fmt.Sprint(p-3)) {
				continue
			}
			for _, wm := range []int{1, 32 << 10, 64 << 10, 128 << 10} {
				vs = append(vs, v{fmt.Sprintf("%s warm=%dK", ir.Profiles[p].Name, wm>>10), p, wm})
			}
		}
		vs = append(vs, v{"auto", ir.AutoProfile, 0})
		hintCost(pr, s, len(raw))
		var mono float64
		for _, x := range vs {
			ir.EmuWarm = x.warm
			size := func(pol ir.Policy) (float64, time.Duration) {
				t0 := time.Now()
				pk, err := ir.Encode(s, ir.Config{Codec: ir.Pred, Policy: pol, Profile: x.prof})
				if err != nil {
					fatal(err)
				}
				return 100 * float64(pk.TotalUnitBytes()+len(pk.Skeleton)) / float64(len(raw)), time.Since(t0)
			}
			m, _ := size(ir.Policy{Kind: ir.PStream})
			a, d := size(pol16)
			b, _ := size(pol4)
			if x.name == "P3 zlib-fast-like" {
				mono = m
			}
			_ = mono
			fmt.Printf("| %s | %d | %s | %.2f | %.2f (+%.2f) | %.2f | %.1f |\n", pr, len(raw), x.name, m, a, a-m, b, float64(len(pb))/(1<<20)/d.Seconds())
		}
		ir.EmuWarm = 0
	}
}

func parseGz(b []byte) (hdr int, raw []byte, err error) {
	g, _, err := deflate.ParseGzip(b, deflate.Options{}, false)
	if err != nil {
		return 0, nil, err
	}
	_ = g
	// header is 10 bytes for gzip -n; strip header and 8-byte trailer
	if len(b) < 18 {
		return 0, nil, fmt.Errorf("short gzip")
	}
	return 10, b[10 : len(b)-8], nil
}

func compressWith(py, name string, plain []byte) ([]byte, error) {
	run := func(name string, args ...string) ([]byte, error) {
		cmd := exec.Command(name, args...)
		cmd.Stdin = bytes.NewReader(plain)
		var out, er bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &er
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("%v: %s", err, er.String())
		}
		return out.Bytes(), nil
	}
	switch {
	case strings.HasPrefix(name, "zlib"):
		return run(py, "-I", "-c", "import sys,zlib\nc=zlib.compressobj(int(sys.argv[1]),8,-15,8,0)\nsys.stdout.buffer.write(c.compress(sys.stdin.buffer.read())+c.flush())", name[4:])
	case strings.HasPrefix(name, "gzip"):
		return run("gzip", "-n", "-"+name[4:], "-c")
	}
	return nil, fmt.Errorf("unknown producer %s", name)
}

// hintCost estimates approach B (store which window positions the compressor did
// not insert): the number of long matches in the 32 KiB before each 16 KiB unit
// start, at a generous 7 bits per boundary gap, against the unit's own size.
func hintCost(name string, s *deflate.Stream, compLen int) {
	lv := 4
	if strings.HasSuffix(name, "2") {
		lv = 5
	} else if strings.HasSuffix(name, "3") {
		lv = 6
	}
	pk, err := ir.Encode(s, ir.Config{Codec: ir.Pred, Policy: ir.Policy{Kind: ir.PTokRun, Target: 16 << 10}, Profile: 3})
	if err != nil {
		return
	}
	type m struct{ pos, n int }
	var ms []m
	pos := 0
	for bi := range s.Blocks {
		for _, t := range s.Blocks[bi].Tokens {
			if t.IsLiteral() {
				pos++
				continue
			}
			if t.Length() > lv {
				ms = append(ms, m{pos, t.Length()})
			}
			pos += t.Length()
		}
	}
	total, k := 0, 0
	for _, st := range pk.UnitPlainStart[1:] {
		for k < len(ms) && ms[k].pos < st-32768 {
			k++
		}
		for j := k; j < len(ms) && ms[j].pos < st; j++ {
			total++
		}
	}
	units := max(len(pk.UnitPlainStart)-1, 1)
	perUnit := float64(total) / float64(units)
	fmt.Printf("hint (approach B) %s: %.0f window boundaries per unit start = ~%.0f B/unit at 7 bits each; units average %.0f compressed B\n\n",
		name, perUnit, perUnit*7/8, float64(compLen)/float64(units))
}
