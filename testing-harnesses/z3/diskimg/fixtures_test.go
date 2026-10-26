package diskimg

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func need(t *testing.T, tool string) {
	t.Helper()
	if _, err := exec.LookPath(tool); err != nil {
		t.Skipf("%s not installed", tool)
	}
}

// smallDisk is a 48 MiB logical disk touching every generator kind and a hole.
func smallDisk() *Disk {
	return &Disk{Size: 48 * MiB, Writes: []Extent{
		{0, 512 * KiB, KMeta, 1}, {2 * MiB, 3 * MiB, KText, 2}, {8 * MiB, 2 * MiB, KRandom, 3},
		{12 * MiB, 3 * MiB, KDup, 4}, {16*MiB + 5*4096, 4096, KPage, 5}, {20 * MiB, 64 * KiB, KZero, 0},
		{30 * MiB, 100 * KiB, KStruct, 6}, {40*MiB - 4096, 8192, KRandom, 7},
	}}
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

// qcowFixture builds a small image with qemu-img; opts are -o options.
func qcowFixture(t *testing.T, d *Disk, opts string, extra ...string) (string, []byte) {
	t.Helper()
	need(t, "qemu-img")
	dir := t.TempDir()
	raw := filepath.Join(dir, "d.raw")
	if err := d.WriteRaw(raw); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(dir, "d.qcow2")
	args := append([]string{"convert", "-f", "raw", "-O", "qcow2", "-o", opts}, extra...)
	run(t, "qemu-img", append(args, raw, img)...)
	b, err := os.ReadFile(img)
	if err != nil {
		t.Fatal(err)
	}
	return img, b
}

func haveVhdx(t *testing.T) {
	t.Helper()
	for _, p := range []string{VhdxGen(), VhdiRead()} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("%s missing (build testing-harnesses/z3/oracle/vhdxgen and vhdiread)", p)
		}
	}
}

// vhdxFixture builds a dynamic VHDX (block 1 MiB unless block != "") from d.
func vhdxFixture(t *testing.T, d *Disk, block string) (string, []byte) {
	t.Helper()
	haveVhdx(t)
	dir := t.TempDir()
	raw := filepath.Join(dir, "d.raw")
	if err := d.WriteRaw(raw); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(dir, "d.vhdx")
	if block == "" {
		block = "1048576"
	}
	run(t, VhdxGen(), "--out", img, "--src", raw, "--block", block)
	b, err := os.ReadFile(img)
	if err != nil {
		t.Fatal(err)
	}
	return img, b
}
