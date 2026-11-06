package simdelta

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os/exec"
	"path/filepath"
	"testing"
)

// Feed the newly encoded Git COPY/ADD program to native Git, independently of
// the laboratory Git decoder. No GPL implementation is copied into the codec.
func TestGitProgramNativeReplay(t *testing.T) {
	rng := rand.New(rand.NewSource(911))
	base := make([]byte, 90000)
	rng.Read(base)
	target := append([]byte("inserted-prefix"), base[71:]...)
	target = append(target, base[:71]...)
	wire, e := Encode("git", [][]byte{base}, target)
	if e != nil {
		t.Fatal(e)
	}
	_, program, e := parse(wire)
	if e != nil {
		t.Fatal(e)
	}
	blobID := func(b []byte) [20]byte { return sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(b))), b...)) }
	header := func(kind, n int) []byte {
		c := byte(kind<<4 | n&15)
		n >>= 4
		if n != 0 {
			c |= 128
		}
		h := []byte{c}
		for n != 0 {
			c = byte(n & 127)
			n >>= 7
			if n != 0 {
				c |= 128
			}
			h = append(h, c)
		}
		return h
	}
	compress := func(b []byte) []byte {
		var out bytes.Buffer
		w := zlib.NewWriter(&out)
		w.Write(b)
		w.Close()
		return out.Bytes()
	}
	pack := []byte("PACK")
	pack = binary.BigEndian.AppendUint32(pack, 2)
	pack = binary.BigEndian.AppendUint32(pack, 2)
	pack = append(pack, header(3, len(base))...)
	pack = append(pack, compress(base)...)
	pack = append(pack, header(7, len(program))...)
	id := blobID(base)
	pack = append(pack, id[:]...)
	pack = append(pack, compress(program)...)
	sum := sha1.Sum(pack)
	pack = append(pack, sum[:]...)
	repo := filepath.Join(t.TempDir(), "native.git")
	if out, e := exec.Command("git", "init", "--bare", repo).CombinedOutput(); e != nil {
		t.Fatalf("git init: %v %s", e, out)
	}
	cmd := exec.Command("git", "-C", repo, "index-pack", "--stdin", "--strict")
	cmd.Stdin = bytes.NewReader(pack)
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("native delta: %v %s", e, out)
	}
	want := blobID(target)
	out, e := exec.Command("git", "-C", repo, "cat-file", "blob", fmt.Sprintf("%x", want)).Output()
	if e != nil || !bytes.Equal(out, target) {
		t.Fatalf("native exact replay: %v", e)
	}
}
