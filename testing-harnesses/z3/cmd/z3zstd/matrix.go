package main

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"zeros3-z3/frontier"
	"zeros3-z3/lab"
	"zeros3-z3/zstdz3"
)

type mrow struct {
	name, setting        string
	size                 int
	parse                string
	exact                string
	fallbackPct          float64
	irPct, irPackedPct   float64
	units                int
	frames, blocks       int
	secs                 float64
	crossCDC, crossIR    int64 // new bytes when added after the base producer's output
	crossIRChunks        int
	notes                string
	plainOK, refDecodeOK bool
}

// producerCmd renders one producer invocation: ref binaries, klauspost levels, zstd:chunked.
func produce(tag string, in, out string) (string, error) {
	switch {
	case strings.HasPrefix(tag, "kp"):
		_, err := run(filepath.Join(toolsRoot(), "z3cprod"), "zstd", "-in", in, "-out", out, "-level", tag[2:])
		return "klauspost/compress v1.18.0 level " + tag[2:] + ", 1 goroutine", err
	case tag == "zc":
		_, err := run(filepath.Join(toolsRoot(), "z3cprod"), "zstdchunked", "-in", in, "-out", out)
		return "containers/storage v1.59.1 zstd:chunked (default level)", err
	}
	p, ok := producers[tag]
	if !ok {
		return "", fmt.Errorf("unknown producer %q", tag)
	}
	return p.cmdline(), p.compress(in, out)
}

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var out, eb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &eb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %v: %w\n%s", name, args, err, eb.String())
	}
	return out.String(), nil
}

func refDecodeSHA(zst string) (string, error) {
	cmd := exec.Command(zstdBin("1.5.7"), "-d", "-q", "-c", zst)
	h := sha256.New()
	cmd.Stdout = h
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// cmdMatrix runs one plaintext through many producers: does each stream parse, decode to
// the same bytes as the reference decoder, replay exactly, how large is the description,
// and how much of it is new after the base producer's stream (and its description) is stored.
func cmdMatrix(args []string) {
	fs := flag.NewFlagSet("matrix", flag.ExitOnError)
	in := fs.String("in", "", "plaintext file")
	dir := fs.String("dir", "/root/z3scratch/c25/matrix", "scratch directory")
	tags := fs.String("tags", "z155l3,z157l3,z155f3,z157f3,z155l1,z157l1,z157l9,z155l19,z157l19,kp1,kp2,kp3,kp4,zc,z157mt", "producers; the first is the base")
	fresh := fs.Bool("fresh", false, "recompress even if the output exists")
	fs.Parse(args)
	must(os.MkdirAll(*dir, 0o755))
	plainSHA := fileSHA(*in)
	pst, _ := os.Stat(*in)
	var base struct{ zst, skel, units []byte }
	var rows []mrow
	for i, tag := range strings.Split(*tags, ",") {
		out := filepath.Join(*dir, tag+".zst")
		desc := "(kept from an earlier run)"
		if st, err := os.Stat(out); err != nil || st.Size() == 0 || *fresh {
			var err error
			desc, err = produce(tag, *in, out)
			must(err)
		} else if p, ok := producers[tag]; ok {
			desc = p.cmdline()
		}
		zst, err := os.ReadFile(out)
		must(err)
		r := mrow{name: tag, setting: desc, size: len(zst)}
		t0 := time.Now()
		var plain, units bytes.Buffer
		res, err := zstdz3.Parse(bytes.NewReader(zst), &plain, &units, zstdz3.Options{})
		r.secs = time.Since(t0).Seconds()
		if err != nil {
			r.parse = "ERR " + err.Error()
			rows = append(rows, r)
			continue
		}
		r.parse = "ok"
		r.plainOK = fmt.Sprintf("%x", sha256.Sum256(plain.Bytes())) == plainSHA
		if ref, err := refDecodeSHA(out); err == nil {
			r.refDecodeOK = ref == plainSHA
		}
		var re bytes.Buffer
		if _, err := zstdz3.Replay(&re, res.Raw, bytes.NewReader(units.Bytes()), bytes.NewReader(plain.Bytes()), zstdz3.Limits{}); err != nil {
			r.exact = "ERR " + err.Error()
		} else if bytes.Equal(re.Bytes(), zst) {
			r.exact = "exact"
		} else {
			r.exact = "DIFFERS"
		}
		s := res.Stats
		r.fallbackPct = 100 * float64(s.StreamBytes[zstdz3.BlockFallback]) / float64(len(zst))
		r.irPct = 100 * float64(s.IRBytes()) / float64(len(zst))
		r.irPackedPct = 100 * float64(lab.PackedLen(res.Raw)+lab.PackedLen(units.Bytes())) / float64(len(zst))
		r.units, r.frames = units.Len(), s.Frames+s.Skippable
		r.blocks = s.Blocks[0] + s.Blocks[1] + s.Blocks[2] + s.Blocks[3]
		r.notes = fmt.Sprintf("blocks raw/rle/xform/fb %v", s.Blocks)
		// reuse against the base producer's stored output only (not against earlier rows)
		if i == 0 {
			base.zst, base.skel, base.units = zst, res.Raw, units.Bytes()
		}
		cc, sc, uc := frontier.NewChain(), frontier.NewChain(), frontier.NewChain()
		cc.AddStream(bytes.NewReader(base.zst))
		sc.AddStream(bytes.NewReader(base.skel))
		uc.AddStream(bytes.NewReader(base.units))
		if i == 0 {
			cc, sc, uc = frontier.NewChain(), frontier.NewChain(), frontier.NewChain()
		}
		c, _ := cc.AddStream(bytes.NewReader(zst))
		a, _ := sc.AddStream(bytes.NewReader(res.Raw))
		bb, _ := uc.AddStream(bytes.NewReader(units.Bytes()))
		r.crossCDC, r.crossIR = c.Incr, a.Incr+bb.Incr
		rows = append(rows, r)
	}
	fmt.Printf("plaintext %s: %d bytes sha %s\n", *in, pst.Size(), plainSHA[:12])
	fmt.Printf("%-8s %9s %-6s %-6s %-6s %-7s %7s %8s %9s %9s %s\n", "producer", "size", "parse", "dec=ref", "replay", "fb%", "IR%", "IRpack%", "newCDC", "newIR", "blocks")
	for _, r := range rows {
		fmt.Printf("%-8s %9d %-6s %-3v/%-3v %-6s %6.2f%% %6.1f%% %7.1f%% %9s %9s %s\n", r.name, r.size, r.parse, r.plainOK, r.refDecodeOK, r.exact, r.fallbackPct, r.irPct, r.irPackedPct, hum(float64(r.crossCDC)), hum(float64(r.crossIR)), r.notes)
	}
	for _, r := range rows {
		fmt.Printf("# %-8s %s\n", r.name, r.setting)
	}
	_ = io.Discard
}
