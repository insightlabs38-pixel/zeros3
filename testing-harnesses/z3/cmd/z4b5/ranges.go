package main

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"time"

	"zeros3-z3/lab"
	"zeros3-z3/mdelta2"
)

// cmdRanges: ranges -store DIR -orig FILE [-n 200]  (random 4KiB/64KiB/1MiB reads of a stored state, verified
// against the original; reports payload bytes reconstructed per request vs the production-CDC chunk bytes).
func cmdRanges(args []string) {
	fl := flag.NewFlagSet("ranges", flag.ExitOnError)
	dir := fl.String("store", "", "store")
	orig := fl.String("orig", "", "original file")
	n := fl.Int("n", 100, "reads per size")
	fl.Parse(args)
	data, err := os.ReadFile(*orig)
	must(err)
	id := mdelta2.FileSHA(data)
	s := mdelta2.StoreDir{Dir: *dir}
	// CDC baseline: compressed bytes of the chunks intersecting each request
	type ch struct{ off, n, packed int64 }
	var chunks []ch
	must(lab.ChunkCDC(bytes.NewReader(data), func(off int64, c []byte) error {
		chunks = append(chunks, ch{off, int64(len(c)), lab.PackedLen(c)})
		return nil
	}))
	rng := rand.New(rand.NewSource(7))
	fmt.Printf("state %s (%d bytes, %d CDC chunks)\n", hex.EncodeToString(id[:])[:12], len(data), len(chunks))
	for _, sz := range []int64{4096, 65536, 1 << 20} {
		var dec, rd, cdc int64
		var dur time.Duration
		for k := 0; k < *n; k++ {
			off := rng.Int63n(int64(len(data)) - sz)
			r := mdelta2.NewStoreResolver(s) // cold: no decoded cache across requests
			c, err := r.LoadContainer(id)
			must(err)
			var buf bytes.Buffer
			t0 := time.Now()
			dcd, err := c.ReadRange(&buf, off, sz, r)
			must(err)
			dur += time.Since(t0)
			if !bytes.Equal(buf.Bytes(), data[off:off+sz]) {
				must(fmt.Errorf("range mismatch at %d", off))
			}
			dec += dcd + r.Decoded
			rd += r.Reads
			for _, q := range chunks {
				if q.off < off+sz && q.off+q.n > off {
					cdc += q.packed
				}
			}
		}
		fmt.Printf("  %7d B requests: payload reconstructed/request %d B (%.1fx), container bytes read/request %d B, CDC compressed chunk bytes/request %d B (%.1fx), %.2f ms/request\n",
			sz, dec/int64(*n), float64(dec)/float64(*n)/float64(sz), rd/int64(*n), cdc/int64(*n), float64(cdc)/float64(*n)/float64(sz), dur.Seconds()*1000/float64(*n))
	}
}
