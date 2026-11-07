package sqfsz3

import (
	"bytes"
	"math/rand"
	"os"
	"testing"
	"time"
)

// TestMutationsTerminate: random 1-3 byte mutations of a real image terminate within the bounds,
// and every mutation Encode accepts rebuilds exactly (the coverage-guided fuzzer is too slow on 36 KB seeds).
func TestMutationsTerminate(t *testing.T) {
	img, _ := os.ReadFile("testdata/small_lz4_b4k.sqsh")
	r := rand.New(rand.NewSource(1))
	lim := Limits{MaxImage: 1 << 22, MaxBlocks: 4096, MaxInodes: 1 << 16, MaxDecoded: 1 << 24}
	n := 6000
	if testing.Short() {
		n = 300
	}
	for i := 0; i < n; i++ {
		c := append([]byte(nil), img...)
		for k := 0; k < 1+r.Intn(3); k++ {
			c[r.Intn(len(c))] = byte(r.Intn(256))
		}
		done := make(chan struct{})
		var rec, plain []byte
		var err error
		go func() { rec, plain, _, err = Encode(c, lim); close(done) }()
		select {
		case <-done:
			if err == nil {
				var out bytes.Buffer
				if e := Decode(&out, rec, plain, lim); e != nil || !bytes.Equal(out.Bytes(), c) {
					t.Fatalf("iteration %d: accepted mutation does not rebuild: %v", i, e)
				}
			}
		case <-time.After(3 * time.Second):
			os.WriteFile("/root/hang.sqsh", c, 0o644)
			t.Fatalf("iteration %d hangs (saved /root/hang.sqsh)", i)
		}
	}
}
