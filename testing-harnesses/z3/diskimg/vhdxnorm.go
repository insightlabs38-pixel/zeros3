package diskimg

import (
	"crypto/sha256"
	"fmt"
	"os"
	"unicode/utf16"
)

// GUIDText renders stored (mixed-endian) GUID bytes as canonical text without braces.
func GUIDText(g [16]byte) string {
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		g[3], g[2], g[1], g[0], g[5], g[4], g[7], g[6], g[8], g[9], g[10], g[11], g[12], g[13], g[14], g[15])
}

func detGUID(seed, label string) (g [16]byte) {
	h := sha256.Sum256([]byte("z3-26/" + seed + "/" + label))
	copy(g[:], h[:16])
	g[7] = g[7]&0x0f | 0x40 // version 4 nibble (in the mixed-endian field)
	g[8] = g[8]&0x3f | 0x80
	return
}

// NormalizeVhdx replaces the per-file random identifiers a producer drew (the two headers' FileWriteGuid and
// DataWriteGuid, and the Virtual Disk ID) with seed-derived values, repairs the header CRC-32C, and (for a
// differencing file) rewrites parent_linkage to the canonical text of the parent's active DataWriteGuid.
// Nothing else is touched. This is a disclosed research normalisation; the result is re-validated with libvhdi.
func NormalizeVhdx(path, seed, parentPath string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	st, _ := f.Stat()
	v, err := ScanVhdx(f, st.Size())
	if err != nil {
		return err
	}
	for i, off := range []int64{64 * KiB, 128 * KiB} {
		b := make([]byte, 4096)
		if _, err := f.ReadAt(b, off); err != nil {
			return err
		}
		fwg, dwg := detGUID(seed, fmt.Sprintf("fwg%d", i)), detGUID(seed, fmt.Sprintf("dwg%d", i))
		if i == v.ActiveHeader {
			dwg = detGUID(seed, "dwg")
		}
		copy(b[16:], fwg[:])
		copy(b[32:], dwg[:])
		clear(b[4:8])
		putLE32(b[4:], crc32Sum(b))
		if _, err := f.WriteAt(b, off); err != nil {
			return err
		}
	}
	id := detGUID(seed, "diskid")
	if _, err := f.WriteAt(id[:], v.fieldOff["diskid"]); err != nil {
		return err
	}
	if v.HasParent {
		pf, err := os.Open(parentPath)
		if err != nil {
			return err
		}
		defer pf.Close()
		pst, _ := pf.Stat()
		pv, err := ScanVhdx(pf, pst.Size())
		if err != nil {
			return err
		}
		old := v.Parent["parent_linkage"]
		txt := "{" + GUIDText(pv.DataWriteGUID) + "}"
		if len(txt) != len(old) {
			return fmt.Errorf("parent_linkage length %d != %d", len(old), len(txt))
		}
		u := utf16.Encode([]rune(txt))
		b := make([]byte, 2*len(u))
		for i, c := range u {
			b[2*i], b[2*i+1] = byte(c), byte(c>>8)
		}
		if _, err := f.WriteAt(b, v.fieldOff["loc:parent_linkage"]); err != nil {
			return err
		}
	}
	return nil
}

// SetVhdxParentRelPath rewrites the relative_path locator entry in place with a string of the same length.
func SetVhdxParentRelPath(path, rel string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	st, _ := f.Stat()
	v, err := ScanVhdx(f, st.Size())
	if err != nil {
		return err
	}
	old, ok := v.Parent["relative_path"]
	if !ok || len([]rune(old)) != len([]rune(rel)) {
		return fmt.Errorf("relative_path %q cannot be replaced by %q in place", old, rel)
	}
	u := utf16.Encode([]rune(rel))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	_, err = f.WriteAt(b, v.fieldOff["loc:relative_path"])
	return err
}

func putLE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}
func putLE64(b []byte, v uint64) { putLE32(b, uint32(v)); putLE32(b[4:], uint32(v>>32)) }
