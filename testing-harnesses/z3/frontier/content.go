package frontier

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"strings"
)

// All content is a pure function of (Gen, Seed, Size): math/rand/v2 PCG is a
// stable, documented generator, so the corpus is identical on every machine.

var vocab = func() []string {
	r := rand.New(rand.NewPCG(0x5a3, 0x77))
	w := make([]string, 2048)
	for i := range w {
		n := 2 + r.IntN(9)
		b := make([]byte, n)
		for j := range b {
			b[j] = byte('a' + r.IntN(26))
		}
		w[i] = string(b)
	}
	return w
}()

func newRNG(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) }

func word(r *rand.Rand) string {
	u := r.Float64()
	return vocab[int(float64(len(vocab))*u*u*u)]
}

func genRand(r *rand.Rand, n int64) []byte {
	b := make([]byte, n)
	i := 0
	for ; i+8 <= len(b); i += 8 {
		binary.LittleEndian.PutUint64(b[i:], r.Uint64())
	}
	for ; i < len(b); i++ {
		b[i] = byte(r.Uint64())
	}
	return b
}

func genText(r *rand.Rand, n int64) []byte {
	var sb strings.Builder
	line := 0
	for int64(sb.Len()) < n {
		w := word(r)
		if line+len(w) > 78 {
			sb.WriteByte('\n')
			line = 0
		}
		if line > 0 {
			sb.WriteByte(' ')
			line++
		}
		sb.WriteString(w)
		line += len(w)
	}
	return []byte(sb.String()[:n])
}

func genJSON(r *rand.Rand, n int64) []byte {
	var sb strings.Builder
	id := r.IntN(1 << 20)
	for int64(sb.Len()) < n {
		id++
		fmt.Fprintf(&sb, `{"id":%d,"name":"%s","tags":["%s","%s"],"score":%d.%02d,"ok":%t}`+"\n",
			id, word(r), word(r), word(r), r.IntN(1000), r.IntN(100), r.IntN(2) == 0)
	}
	return []byte(sb.String()[:n])
}

func genLog(r *rand.Rand, n int64) []byte {
	var sb strings.Builder
	t := baseMtime + int64(r.IntN(1<<20))
	lv := []string{"INFO ", "INFO ", "INFO ", "WARN ", "DEBUG"}
	for int64(sb.Len()) < n {
		t += int64(r.IntN(3))
		fmt.Fprintf(&sb, "ts=%d %s svc-%02d request id=%08x path=/api/%s status=%d dur=%dms\n",
			t, lv[r.IntN(len(lv))], r.IntN(16), r.Uint32()&0xfff0fff, word(r), 200+100*(r.IntN(10)/9), r.IntN(40))
	}
	return []byte(sb.String()[:n])
}

func genZeroish(r *rand.Rand, n int64) []byte {
	blk := genText(r, 4096)
	b := make([]byte, n)
	for i := int64(0); i < n; i += 4096 {
		copy(b[i:], blk)
		if (i/4096)%61 == 0 && i+16 < n {
			binary.LittleEndian.PutUint64(b[i:], r.Uint64())
		}
	}
	return b
}

// genRecords: independent 4 KiB records so an in-place patch never shifts content.
func genRecords(seed uint64, n int64) []byte {
	b := make([]byte, n)
	for i := int64(0); i*4096 < n; i++ {
		r := newRNG(seed*1_000_003 + uint64(i))
		rec := genText(r, 4096)
		binary.LittleEndian.PutUint64(rec, uint64(i))
		copy(b[i*4096:], rec)
	}
	return b
}

// Bytes returns the full logical content of a non-sparse body.
func (b *Body) Bytes() []byte {
	r := newRNG(b.Seed)
	var out []byte
	switch b.Gen {
	case "rand":
		out = genRand(r, b.Size)
	case "text":
		out = genText(r, b.Size)
	case "json":
		out = genJSON(r, b.Size)
	case "log":
		out = genLog(r, b.Size)
	case "zeroish":
		out = genZeroish(r, b.Size)
	case "records":
		out = genRecords(b.Seed, b.Size)
	default:
		panic("frontier: unknown generator " + b.Gen)
	}
	for _, p := range b.Patches {
		if p.Off < 0 || p.Off+int64(p.Len) > int64(len(out)) {
			continue
		}
		copy(out[p.Off:], genRand(newRNG(p.Seed), int64(p.Len)))
	}
	return out
}

// ExtentBytes returns the data of one sparse extent.
func (x Extent) Bytes() []byte {
	if x.Zero {
		return make([]byte, x.Len)
	}
	return genRand(newRNG(x.Seed), x.Len)
}
