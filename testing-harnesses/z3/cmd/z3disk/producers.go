package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"zeros3-z3/diskimg"
)

// Lane is one producer configuration.
type Lane struct {
	Name     string
	Fmt      string // qcow2 | vhdx
	QOpts    string
	Compress bool
	Block    int64
}

var (
	laneQcow = Lane{Name: "qcow2", Fmt: "qcow2", QOpts: "cluster_size=65536"}
	laneXL2  = Lane{Name: "qcow2-xl2", Fmt: "qcow2", QOpts: "cluster_size=65536,extended_l2=on"}
	laneZlib = Lane{Name: "qcow2-zlib", Fmt: "qcow2", QOpts: "cluster_size=65536", Compress: true}
	laneVhdx = Lane{Name: "vhdx-2M", Fmt: "vhdx", Block: 2 << 20}
	laneV1M  = Lane{Name: "vhdx-1M", Fmt: "vhdx", Block: 1 << 20}
)

func sh(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %v\n%s", name, strings.Join(args, " "), err, tail(string(out), 600))
	}
	return string(out), nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// Parent names the parent of a differencing build.
type Parent struct {
	Img, Raw string
	Delta    []diskimg.Extent // writes applied on top of the parent's logical state (QCOW2 children are written, not converted)
	Size     int64
}

// build produces an image of lane l from raw (a sparse raw file holding the final logical state).
func build(l Lane, raw, out, seed string, par *Parent, order string) error {
	switch l.Fmt {
	case "qcow2":
		args := []string{"convert", "-f", "raw", "-O", "qcow2", "-o", l.QOpts}
		if l.Compress {
			args = append(args, "-c")
		}
		if par != nil {
			return buildQcowChild(l, out, par)
		}
		_, err := sh("qemu-img", append(args, raw, out)...)
		return err
	case "vhdx":
		args := []string{"--out", out, "--src", raw, "--block", fmt.Sprint(l.Block)}
		pimg := ""
		if par != nil {
			pimg = par.Img
			args = append(args, "--parent", par.Img, "--parent-rel", filepath.Base(par.Img), "--parent-raw", par.Raw)
		}
		if order != "" {
			args = append(args, "--order", order, "--seed", "26")
		}
		if _, err := sh(diskimg.VhdxGen(), args...); err != nil {
			return err
		}
		return diskimg.NormalizeVhdx(out, seed, pimg)
	}
	return fmt.Errorf("lane %s", l.Name)
}

// buildHistory produces the same logical disk through a different write history. mode "desc" writes the disk's data
// from the high offsets downwards (reverse-grouped); mode "shuf" writes it in a fixed pseudo-random order of 2 MiB groups.
func buildHistory(l Lane, d *diskimg.Disk, tmp, out, seed, mode string) error {
	if l.Fmt == "vhdx" {
		raw := filepath.Join(tmp, "hist.raw")
		defer os.Remove(raw)
		if err := d.WriteRaw(raw); err != nil {
			return err
		}
		return build(l, raw, out, seed, nil, mode)
	}
	if _, err := sh("qemu-img", "create", "-q", "-f", "qcow2", "-o", l.QOpts, out, fmt.Sprint(d.Size)); err != nil {
		return err
	}
	ps := d.Flatten()
	const ngroups = 24
	group := func(off int64) int { return int(mixSeed(uint64(off>>21)) % ngroups) }
	var order [][]diskimg.Piece
	if mode == "desc" {
		cuts := []int64{2048 * diskimg.MiB, 1024 * diskimg.MiB, 512 * diskimg.MiB, 0}
		for _, c := range cuts {
			var g []diskimg.Piece
			for _, p := range ps {
				if p.Off >= c && (len(order) == 0 || p.Off < prevCut(cuts, c)) {
					g = append(g, p)
				}
			}
			order = append(order, g)
		}
	} else {
		order = make([][]diskimg.Piece, ngroups)
		for _, p := range ps {
			g := group(p.Off)
			order[g] = append(order[g], p)
		}
	}
	raw := filepath.Join(tmp, "hist.raw")
	defer os.Remove(raw)
	for _, g := range order {
		if len(g) == 0 {
			continue
		}
		if err := (&diskimg.Disk{Size: d.Size, Writes: g}).WriteRaw(raw); err != nil {
			return err
		}
		if _, err := sh("qemu-img", "convert", "-n", "--target-is-zero", "-f", "raw", "-O", "qcow2", raw, out); err != nil {
			return err
		}
	}
	return nil
}

func prevCut(cuts []int64, c int64) int64 {
	for i, x := range cuts {
		if x == c && i > 0 {
			return cuts[i-1]
		}
	}
	return 1 << 62
}

// verify proves the image's guest-visible disk equals the generator's, via an implementation independent of the scanners.
func verify(l Lane, img, raw string, d *diskimg.Disk, chain []string) error {
	switch l.Fmt {
	case "qcow2":
		if out, err := sh("qemu-img", "compare", "-f", "raw", "-F", "qcow2", raw, img); err != nil {
			return fmt.Errorf("qemu-img compare: %v %s", err, out)
		}
		if out, err := sh("qemu-img", "check", "-f", "qcow2", img); err != nil {
			return fmt.Errorf("qemu-img check: %v %s", err, out)
		}
		return nil
	case "vhdx":
		want, err := d.SHA256()
		if err != nil {
			return err
		}
		cmd := exec.Command(diskimg.VhdiRead(), append([]string{img}, chain...)...)
		out, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		if err := cmd.Start(); err != nil {
			return err
		}
		h := sha256.New()
		n, _ := io.Copy(h, out)
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("vhdiread: %v", err)
		}
		var got [32]byte
		copy(got[:], h.Sum(nil))
		if got != want || n != d.Size {
			return fmt.Errorf("libvhdi logical disk differs (len %d)", n)
		}
		return nil
	}
	return nil
}

func fileSHA(path string) ([32]byte, int64, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, 0, 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	var s [32]byte
	copy(s[:], h.Sum(nil))
	var st syscall.Stat_t
	_ = syscall.Stat(path, &st)
	return s, n, st.Blocks * 512, err
}

// buildQcowChild makes a minimal overlay: `qemu-img convert -B` records the backing name but still writes the whole disk, and a
// safe `rebase` keeps already-allocated identical clusters, so the child is created empty on the parent and only the delta is written.
func buildQcowChild(l Lane, out string, par *Parent) error {
	if _, err := sh("qemu-img", "create", "-q", "-f", "qcow2", "-o", l.QOpts, "-b", par.Img, "-F", "qcow2", out); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(out), "delta")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	args := []string{"-f", "qcow2"}
	for i, w := range par.Delta {
		if w.Kind == diskimg.KZero {
			args = append(args, "-c", fmt.Sprintf("write -z %d %d", w.Off, w.Len))
			continue
		}
		p := filepath.Join(tmp, fmt.Sprint(i))
		buf := make([]byte, w.Len)
		diskimg.FillExtent(buf, w, w.Off)
		if err := os.WriteFile(p, buf, 0o644); err != nil {
			return err
		}
		args = append(args, "-c", fmt.Sprintf("write -s %s %d %d", p, w.Off, w.Len))
	}
	_, err = sh("qemu-io", append(args, out)...)
	return err
}
