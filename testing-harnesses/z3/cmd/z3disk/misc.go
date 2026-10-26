package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"zeros3-z3/diskimg"
)

// diffBytes counts differing bytes between two files and attributes them to a's region classes.
func diffBytes(a, b string) (int64, map[string]int64, error) {
	fa, err := os.Open(a)
	if err != nil {
		return 0, nil, err
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return 0, nil, err
	}
	defer fb.Close()
	regs, _, _, err := scanAny(a)
	if err != nil {
		return 0, nil, err
	}
	by := map[string]int64{}
	var total int64
	ba, bb := make([]byte, MiB), make([]byte, MiB)
	for off := int64(0); ; off += MiB {
		na, ea := io.ReadFull(fa, ba)
		nb, _ := io.ReadFull(fb, bb)
		n := max(na, nb)
		for i := 0; i < n; i++ {
			var x, y byte
			if i < na {
				x = ba[i]
			}
			if i < nb {
				y = bb[i]
			}
			if x != y {
				total++
				attribute(regs, off+int64(i), 1, by)
			}
		}
		if ea != nil {
			break
		}
	}
	return total, by, nil
}

func runDeterminism(e *Env, lanes []Lane) error {
	d := familyA()[0].Disk
	raw := filepath.Join(e.dir("raw"), "det.raw")
	if err := d.WriteRaw(raw); err != nil {
		return err
	}
	defer os.Remove(raw)
	for _, l := range lanes {
		dir := e.dir("det", l.Name)
		a, b := filepath.Join(dir, "a."+l.Fmt), filepath.Join(dir, "b."+l.Fmt)
		var err error
		if l.Fmt == "vhdx" { // raw producer output, before normalisation
			args := func(out string) []string {
				return []string{"--out", out, "--src", raw, "--block", fmt.Sprint(l.Block)}
			}
			if _, err = sh(diskimg.VhdxGen(), args(a)...); err == nil {
				_, err = sh(diskimg.VhdxGen(), args(b)...)
			}
		} else {
			if err = build(l, raw, a, "", nil, ""); err == nil {
				err = build(l, raw, b, "", nil, "")
			}
		}
		if err != nil {
			return err
		}
		sa, _, _, _ := fileSHA(a)
		sb, _, _, _ := fileSHA(b)
		n, by, _ := diffBytes(a, b)
		fmt.Printf("%-11s R0 twice (producer output): identical=%v differing bytes=%d by class=%v\n", l.Name, sa == sb, n, by)
		if l.Fmt == "vhdx" {
			diskimg.NormalizeVhdx(a, "det", "")
			diskimg.NormalizeVhdx(b, "det", "")
			sa, _, _, _ = fileSHA(a)
			sb, _, _, _ = fileSHA(b)
			fmt.Printf("%-11s after normalising headers' Write GUIDs, Virtual Disk ID and CRCs: identical=%v\n", l.Name, sa == sb)
		}
		os.Remove(a)
		os.Remove(b)
	}
	return nil
}

func chainSHA(img string, parents ...string) ([32]byte, error) {
	cmd := exec.Command(diskimg.VhdiRead(), append([]string{img}, parents...)...)
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return [32]byte{}, err
	}
	h := sha256.New()
	io.Copy(h, out)
	err := cmd.Wait()
	var s [32]byte
	copy(s[:], h.Sum(nil))
	return s, err
}

// runParentPath changes only the recorded parent location of C1 and shows image bytes change while the guest disk does not.
func runParentPath(e *Env, lanes []Lane) error {
	for _, l := range lanes {
		dir := e.dir("B", l.Name)
		c1, b0 := filepath.Join(dir, "C1."+l.Fmt), filepath.Join(dir, "B0."+l.Fmt)
		pdir := e.dir("pp", l.Name)
		c1p, b0p := filepath.Join(pdir, "C1."+l.Fmt), filepath.Join(pdir, "Bx."+l.Fmt)
		os.Remove(b0p)
		if err := os.Link(b0, b0p); err != nil {
			return err
		}
		in, _ := os.ReadFile(c1)
		os.WriteFile(c1p, in, 0o644)
		switch l.Fmt {
		case "qcow2":
			if _, err := sh("qemu-img", "rebase", "-u", "-f", "qcow2", "-F", "qcow2", "-b", b0p, c1p); err != nil {
				return err
			}
			out, err := sh("qemu-img", "compare", "-f", "qcow2", "-F", "qcow2", c1, c1p)
			fmt.Printf("%-11s guest disks identical (qemu-img compare): %v %s", l.Name, err == nil, out)
		case "vhdx":
			if err := diskimg.SetVhdxParentRelPath(c1p, "Bx.vhdx"); err != nil {
				return err
			}
			s1, err1 := chainSHA(c1, b0)
			s2, err2 := chainSHA(c1p, b0p)
			fmt.Printf("%-11s guest disks identical (libvhdi chain read): %v errs=%v/%v\n", l.Name, s1 == s2, err1, err2)
		}
		n, by, _ := diffBytes(c1, c1p)
		fmt.Printf("%-11s parent-path-only change: %d image bytes differ, by class %v\n", l.Name, n, by)
		os.RemoveAll(pdir)
	}
	return nil
}
