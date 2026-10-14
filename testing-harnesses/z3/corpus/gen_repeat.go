package corpus

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Z3-08E: deterministic plaintexts whose LZ77 matches reuse distances (record
// strides, periods, recurring phrases), unlike the random-copy synthetic text.
// They exercise the predictor's repeat-distance context. Pure integer logic.

// RepeatKinds lists the plaintext generators.
func RepeatKinds() []string {
	return []string{"phrases", "records", "periodic", "source", "json", "logs", "binary"}
}

var words = func() []string {
	r := NewRNG(1, "repeat-words")
	w := make([]string, 400)
	for i := range w {
		n := 3 + r.Intn(9)
		b := make([]byte, n)
		for j := range b {
			b[j] = byte('a' + r.Intn(26))
		}
		w[i] = string(b)
	}
	return w
}()

// zipf draws a Zipf-like index in [0, n).
func zipf(r *RNG, n int) int {
	x := r.Intn(n)
	y := r.Intn(n)
	return min(x, y) * min(x, y) / n
}

// RepeatPlain returns about n bytes of the given kind.
func RepeatPlain(kind string, seed uint64, n int) ([]byte, error) {
	r := NewRNG(seed, "repeat-"+kind)
	var b strings.Builder
	switch kind {
	case "phrases": // prose built from a few hundred recurring phrases
		phr := make([]string, 300)
		for i := range phr {
			k := 2 + r.Intn(5)
			ws := make([]string, k)
			for j := range ws {
				ws[j] = words[r.Intn(len(words))]
			}
			phr[i] = strings.Join(ws, " ")
		}
		for b.Len() < n {
			b.WriteString(phr[zipf(r, len(phr))])
			if r.Intn(5) == 0 {
				b.WriteString(". ")
			} else {
				b.WriteByte(' ')
			}
		}
	case "records": // fixed 64-byte records: constant skeleton, a few varying fields
		const rec = 64
		for b.Len() < n {
			row := make([]byte, rec)
			copy(row, "REC:0001 TYPE=ORDER STATUS=OPEN CURRENCY=EUR REGION=EMEA FLAGS=0000")
			put := func(off int, s string) { copy(row[off:], s) }
			put(4, fmt.Sprintf("%04d", r.Intn(10000)))
			put(15, []string{"ORDER", "REFND", "QUOTE", "AUDIT"}[r.Intn(4)])
			put(28, []string{"OPEN", "SHIP", "DONE", "HOLD"}[zipf(r, 4)])
			put(60, fmt.Sprintf("%04x", r.Intn(1<<16)&0xff0f))
			b.Write(row)
		}
	case "periodic": // 4096-byte blocks repeated with sparse byte edits
		blk := r.Bytes(4096)
		for b.Len() < n {
			b.Write(blk)
			for i := 0; i < 3; i++ {
				blk[r.Intn(len(blk))] = byte(r.Intn(256))
			}
		}
	case "source": // C-like source: recurring templates and identifiers
		ids := words[:120]
		for b.Len() < n {
			name := ids[zipf(r, len(ids))]
			t := ids[zipf(r, len(ids))]
			fmt.Fprintf(&b, "static int %s_%s(struct ctx *c, const char *%s, size_t n)\n{\n", name, t, ids[r.Intn(len(ids))])
			for i := 0; i < 2+r.Intn(6); i++ {
				v := ids[zipf(r, len(ids))]
				switch r.Intn(4) {
				case 0:
					fmt.Fprintf(&b, "\tif (c->%s == NULL)\n\t\treturn -EINVAL;\n", v)
				case 1:
					fmt.Fprintf(&b, "\tfor (size_t i = 0; i < n; i++)\n\t\tc->%s[i] = %s(c->%s[i], %d);\n", v, ids[zipf(r, len(ids))], v, r.Intn(64))
				case 2:
					fmt.Fprintf(&b, "\tret = %s_%s(c, %s, n);\n\tif (ret < 0)\n\t\tgoto out;\n", name, v, t)
				default:
					fmt.Fprintf(&b, "\t/* %s: %s %s */\n", v, words[r.Intn(len(words))], words[r.Intn(len(words))])
				}
			}
			b.WriteString("out:\n\treturn ret;\n}\n\n")
		}
	case "json": // JSON lines with a fixed schema
		for i := 0; b.Len() < n; i++ {
			fmt.Fprintf(&b, `{"id":%d,"user":{"name":"%s","tier":"%s"},"event":"%s","ok":%v,"latency_ms":%d,"tags":["%s","%s"]}`+"\n",
				100000+i, words[zipf(r, 80)], []string{"free", "pro", "team"}[zipf(r, 3)], []string{"login", "view", "click", "purchase"}[zipf(r, 4)],
				r.Intn(10) != 0, r.Intn(900), words[zipf(r, 50)], words[zipf(r, 50)])
		}
	case "logs": // access/application log lines
		ts := 1700000000
		for b.Len() < n {
			ts += 1 + r.Intn(3)
			fmt.Fprintf(&b, "2023-11-14T22:%02d:%02dZ host-%02d app[%d]: %s %s status=%d bytes=%d path=/api/v1/%s/%s\n",
				ts/60%60, ts%60, r.Intn(8), 1000+r.Intn(4), []string{"GET", "POST", "GET", "GET"}[r.Intn(4)], "request", []int{200, 200, 200, 304, 404, 500}[zipf(r, 6)],
				r.Intn(50000), words[zipf(r, 40)], words[zipf(r, 40)])
		}
	case "binary": // struct array with pointer-like fields and stride-N tables
		var tmp [8]byte
		base := uint64(0x00007f0000000000)
		for i := 0; b.Len() < n; i++ {
			binary.LittleEndian.PutUint64(tmp[:], base+uint64(i)*48)
			b.Write(tmp[:])
			binary.LittleEndian.PutUint32(tmp[:4], uint32(i))
			b.Write(tmp[:4])
			binary.LittleEndian.PutUint32(tmp[:4], uint32(r.Intn(16)))
			b.Write(tmp[:4])
			b.Write([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
			binary.LittleEndian.PutUint64(tmp[:], uint64(zipf(r, 64))*4096)
			b.Write(tmp[:])
			b.Write(tmp[:4])
			b.Write([]byte{1, 0, 0, 0})
		}
	default:
		return nil, fmt.Errorf("corpus: unknown repeat kind %q", kind)
	}
	return []byte(b.String()[:min(n, b.Len())]), nil
}

// RepeatRevisions returns revs+1 revisions of a RepeatPlain text: each revision
// splices a short fresh fragment of the same kind into the middle.
func RepeatRevisions(kind string, seed uint64, n, revs int) ([][]byte, error) {
	base, err := RepeatPlain(kind, seed, n)
	if err != nil {
		return nil, err
	}
	out := [][]byte{base}
	cur := base
	for i := 1; i <= revs; i++ {
		frag, _ := RepeatPlain(kind, seed+uint64(i)*7919, 600)
		mid := len(cur) / 2
		cur = splice(cur, mid, 0, frag)
		out = append(out, cur)
	}
	return out, nil
}
