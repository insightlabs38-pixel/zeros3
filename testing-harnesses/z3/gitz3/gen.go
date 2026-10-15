package gitz3

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"sort"
)

// Deterministic synthetic repository content. Everything derives from an
// explicit seed through SplitMix64; no floating point, no time, no map order.

type Rng struct{ s uint64 }

func NewRng(seed uint64) *Rng { return &Rng{s: seed} }

func (r *Rng) U64() uint64 {
	r.s += 0x9e3779b97f4a7c15
	z := r.s
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (r *Rng) Intn(n int) int { return int(r.U64() % uint64(n)) }

var vocab = []string{"alloc", "buffer", "count", "delta", "entry", "flush", "get", "hash", "index", "join", "key", "len", "map", "node", "open", "pack", "queue", "read", "size", "tree", "unref", "value", "walk", "xfer", "yield", "zero", "static", "int", "void", "const", "return", "if", "else", "for", "while", "struct", "char", "unsigned", "long"}

// TextFile returns a pseudo source file of about size bytes.
func TextFile(r *Rng, size int) []byte {
	var b bytes.Buffer
	for b.Len() < size {
		n := 2 + r.Intn(7)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(vocab[r.Intn(len(vocab))])
			if r.Intn(5) == 0 {
				fmt.Fprintf(&b, "_%d", r.Intn(64))
			}
		}
		b.WriteString(";\n")
	}
	return b.Bytes()[:size]
}

// BinFile returns binary-like data: structured records (headers + small
// integers) mixed with random runs, about size bytes.
func BinFile(r *Rng, size int) []byte {
	b := make([]byte, 0, size)
	for len(b) < size {
		switch r.Intn(3) {
		case 0: // table of 32-bit counters
			for i := 0; i < 64; i++ {
				v := uint32(r.Intn(1 << 12))
				b = append(b, byte(v), byte(v>>8), 0, 0)
			}
		case 1: // random run
			for i := 0; i < 256; i++ {
				b = append(b, byte(r.U64()))
			}
		default: // zero padding
			b = append(b, make([]byte, 128)...)
		}
	}
	return b[:size]
}

// EditText replaces a few lines near fraction at (0..1) of b.
func EditText(r *Rng, b []byte, at float64, lines int) []byte {
	pos := int(float64(len(b)) * at)
	for pos > 0 && pos < len(b) && b[pos-1] != '\n' {
		pos++
	}
	if pos >= len(b) {
		pos = len(b)
	}
	end := pos
	for l := 0; l < lines && end < len(b); l++ {
		for end < len(b) && b[end] != '\n' {
			end++
		}
		if end < len(b) {
			end++
		}
	}
	ins := TextFile(r, 40*lines)
	out := append(append(append([]byte(nil), b[:pos]...), ins...), b[end:]...)
	return out
}

// EditSparse overwrites n runs of 8 bytes at random positions.
func EditSparse(r *Rng, b []byte, n int) []byte {
	out := append([]byte(nil), b...)
	for i := 0; i < n; i++ {
		p := r.Intn(len(out) - 8)
		for j := 0; j < 8; j++ {
			out[p+j] = byte(r.U64())
		}
	}
	return out
}

// Snap is a snapshot: path -> content.
type Snap map[string][]byte

func (t Snap) Clone() Snap {
	o := make(Snap, len(t))
	for k, v := range t {
		o[k] = v
	}
	return o
}

func (t Snap) paths() []string {
	ps := make([]string, 0, len(t))
	for p := range t {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	return ps
}

// History is a commit graph of full-tree snapshots.
type History struct {
	Trees   []Snap
	Parents [][]int
}

func (h *History) Add(t Snap, parents ...int) int {
	h.Trees = append(h.Trees, t)
	h.Parents = append(h.Parents, parents)
	return len(h.Trees) - 1
}

// BaseSnap is the common starting point of the synthetic families.
func BaseSnap(seed uint64, scale int) Snap {
	r := NewRng(seed)
	t := Snap{}
	for i := 0; i < 24; i++ {
		t[fmt.Sprintf("src/mod%02d/file%02d.c", i%6, i)] = TextFile(r, (4+r.Intn(12))<<10)
	}
	t["docs/big.txt"] = TextFile(r, 256<<10*scale)
	t["assets/blob.bin"] = BinFile(r, 1<<20*scale)
	for i := 0; i < 60; i++ {
		t[fmt.Sprintf("data/tiny/%03d.txt", i)] = TextFile(r, 60+r.Intn(120))
	}
	return t
}

// Evolve makes one realistic small revision: edits three source files, touches
// one tiny file, and occasionally adds a source file.
func Evolve(r *Rng, t Snap) Snap {
	n := t.Clone()
	ps := t.paths()
	for k := 0; k < 3; k++ {
		for {
			p := ps[r.Intn(len(ps))]
			if len(p) > 4 && p[:4] == "src/" {
				n[p] = EditText(r, n[p], float64(r.Intn(100))/100, 1+r.Intn(3))
				break
			}
		}
	}
	n[fmt.Sprintf("data/tiny/%03d.txt", r.Intn(60))] = TextFile(r, 80+r.Intn(100))
	if r.Intn(4) == 0 {
		n[fmt.Sprintf("src/mod%02d/new%03d.c", r.Intn(6), r.Intn(1000))] = TextFile(r, (2+r.Intn(5))<<10)
	}
	return n
}

// LinearHistory builds base + n evolved revisions.
func LinearHistory(seed uint64, scale, n int) *History {
	h := &History{}
	r := NewRng(seed ^ 0xa5a5)
	t := BaseSnap(seed, scale)
	prev := h.Add(t)
	for i := 0; i < n; i++ {
		t = Evolve(r, t)
		prev = h.Add(t, prev)
	}
	return h
}

// Stream writes the history as a git fast-import stream. Commit i has a fixed
// time and identity; trees are emitted as full snapshots (deleteall + M), blobs
// once per distinct content.
func (h *History) Stream(w io.Writer, branch string) error {
	marks := map[[32]byte]int{}
	next := 1
	mark := func(b []byte) (int, bool) {
		s := sha256.Sum256(b)
		if m, ok := marks[s]; ok {
			return m, false
		}
		marks[s] = next
		next++
		return next - 1, true
	}
	commitMark := make([]int, len(h.Trees))
	for i, t := range h.Trees {
		for _, p := range t.paths() {
			m, isNew := mark(t[p])
			if isNew {
				fmt.Fprintf(w, "blob\nmark :%d\ndata %d\n", m, len(t[p]))
				w.Write(t[p])
				fmt.Fprint(w, "\n")
			}
		}
		cm := next
		next++
		commitMark[i] = cm
		msg := fmt.Sprintf("rev %d\n", i)
		ts := 1700000000 + 3600*i
		fmt.Fprintf(w, "commit refs/heads/%s\nmark :%d\nauthor Z3 Lab <z3@example.invalid> %d +0000\ncommitter Z3 Lab <z3@example.invalid> %d +0000\ndata %d\n%s", branch, cm, ts, ts, len(msg), msg)
		for k, par := range h.Parents[i] {
			if k == 0 {
				fmt.Fprintf(w, "from :%d\n", commitMark[par])
			} else {
				fmt.Fprintf(w, "merge :%d\n", commitMark[par])
			}
		}
		fmt.Fprint(w, "deleteall\n")
		for _, p := range t.paths() {
			m, _ := mark(t[p])
			fmt.Fprintf(w, "M 100644 :%d %s\n", m, p)
		}
		fmt.Fprint(w, "\n")
	}
	_, err := fmt.Fprint(w, "done\n")
	return err
}
