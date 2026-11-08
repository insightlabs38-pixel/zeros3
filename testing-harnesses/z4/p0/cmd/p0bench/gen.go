package main

// Deterministic, random-access content. A logical object is a list of
// segments over 4 KiB blocks; block i of stream (kind,seed) is a pure function
// of (kind,seed,i), so any byte range can be regenerated and verified without
// storing the object. "Similar revisions" are segment lists that reuse the base
// stream and splice in fresh blocks (insertions shift offsets, which exercises
// content-defined chunking rather than fixed-size dedup).

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/rand/v2"
	"os"
	"sort"
	"sync"
)

const blk = 4096

type seg struct {
	Seed uint64 `json:"seed"`
	Off  int64  `json:"off"` // byte offset into stream (multiple of blk, except tail)
	Len  int64  `json:"len"`
}

type Content struct {
	Kind string `json:"kind"` // random | text | runs | file
	Segs []seg  `json:"segs"`
	Size int64  `json:"size"`
	Path string `json:"path,omitempty"` // Kind=="file": corpus file (read-only, never modified)
}

var fileMu sync.Mutex
var fileHandles = map[string]*os.File{}

func openShared(p string) *os.File {
	fileMu.Lock()
	defer fileMu.Unlock()
	if f, ok := fileHandles[p]; ok {
		return f
	}
	f, err := os.Open(p)
	if err != nil {
		panic(err)
	}
	fileHandles[p] = f
	return f
}

func NewFileContent(p string) Content {
	fi, err := os.Stat(p)
	if err != nil {
		panic(err)
	}
	return Content{Kind: "file", Path: p, Size: fi.Size()}
}

var words = []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet", "kilo", "lima", "mike", "november", "oscar", "papa", "quebec", "romeo", "sierra", "tango", "uniform", "victor", "whiskey", "xray", "yankee", "zulu", "object", "store", "chunk", "manifest", "journal", "content", "address", "digest", "pack", "record", "index", "level", "tier", "hot", "warm", "cold"}

func genBlock(kind string, seed uint64, i int64, dst []byte) {
	var s [32]byte
	for k := 0; k < 8; k++ {
		s[k] = byte(seed >> (8 * k))
		s[8+k] = byte(uint64(i) >> (8 * k))
	}
	s[16] = 0x5a
	r := rand.NewChaCha8(s)
	switch kind {
	case "text":
		// word salad with ~5 bits/word entropy: compresses ~3-4x with DEFLATE
		n := 0
		for n < len(dst) {
			w := words[r.Uint64()%uint64(len(words))]
			n += copy(dst[n:], w)
			if n < len(dst) {
				dst[n] = ' '
				n++
			}
		}
	case "runs":
		// long runs of few distinct bytes: highly compressible (>50x)
		n := 0
		for n < len(dst) {
			v := byte(r.Uint64() % 4)
			l := 256 + int(r.Uint64()%2048)
			for j := 0; j < l && n < len(dst); j++ {
				dst[n] = v
				n++
			}
		}
	default:
		r.Read(dst)
	}
}

func NewContent(kind string, seed uint64, size int64) Content {
	return Content{Kind: kind, Size: size, Segs: []seg{{Seed: seed, Off: 0, Len: size}}}
}

// Revise returns a new content derived from base: edits of ~editFrac of the
// bytes, in 4-16 KiB regions, half in-place replacements, half insertions.
func Revise(base Content, rev int, editFrac float64) Content {
	r := rand.New(rand.NewPCG(base.Segs[0].Seed, uint64(rev)*7919+1))
	nblk := (base.Size + blk - 1) / blk
	nedit := int(float64(nblk) * editFrac / 2)
	if nedit < 1 {
		nedit = 1
	}
	pos := make([]int64, nedit)
	for i := range pos {
		pos[i] = r.Int64N(nblk)
	}
	sort.Slice(pos, func(i, j int) bool { return pos[i] < pos[j] })
	var out []seg
	cur := int64(0) // byte offset in base
	fresh := base.Segs[0].Seed ^ (uint64(rev) << 40) ^ 0x9e3779b97f4a7c15
	var outLen int64
	add := func(s seg) {
		if s.Len > 0 {
			out = append(out, s)
			outLen += s.Len
		}
	}
	for k, p := range pos {
		off := p * blk
		if off < cur {
			continue
		}
		add(seg{Seed: base.Segs[0].Seed, Off: cur, Len: off - cur})
		n := int64(1+r.IntN(4)) * blk
		fseed := fresh + uint64(k)
		if r.IntN(2) == 0 { // replace n bytes
			if off+n > base.Size {
				n = base.Size - off
			}
			add(seg{Seed: fseed | 1<<63, Off: 0, Len: n})
			cur = off + n
		} else { // insert n bytes, base continues
			add(seg{Seed: fseed | 1<<63, Off: 0, Len: n})
			cur = off
		}
	}
	add(seg{Seed: base.Segs[0].Seed, Off: cur, Len: base.Size - cur})
	return Content{Kind: base.Kind, Segs: out, Size: outLen}
}

// fresh-seed marker: bit 63 set means "inserted stream" - same generator, own seed space.
func (c *Content) ReadAt(p []byte, off int64) (int, error) {
	if off >= c.Size {
		return 0, io.EOF
	}
	if c.Kind == "file" {
		n, err := openShared(c.Path).ReadAt(p, off)
		if err == io.EOF && n == len(p) {
			err = nil
		}
		return n, err
	}
	n := 0
	var buf [blk]byte
	var base int64
	for _, s := range c.Segs {
		if off+int64(n) >= base+s.Len {
			base += s.Len
			continue
		}
		for n < len(p) && off+int64(n) < base+s.Len {
			pos := off + int64(n) - base + s.Off // position in stream
			bi := pos / blk
			genBlock(c.Kind, s.Seed, bi, buf[:])
			inb := int(pos - bi*blk)
			avail := int64(blk - inb)
			if rem := base + s.Len - (off + int64(n)); avail > rem {
				avail = rem
			}
			if avail > int64(len(p)-n) {
				avail = int64(len(p) - n)
			}
			copy(p[n:], buf[inb:inb+int(avail)])
			n += int(avail)
		}
		base += s.Len
		if n == len(p) {
			break
		}
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

type contentReader struct {
	c   *Content
	off int64
}

func (c *Content) NewReader() io.Reader { return &contentReader{c: c} }
func (r *contentReader) Read(p []byte) (int, error) {
	if r.off >= r.c.Size {
		return 0, io.EOF
	}
	if int64(len(p)) > r.c.Size-r.off {
		p = p[:r.c.Size-r.off]
	}
	n, err := r.c.ReadAt(p, r.off)
	r.off += int64(n)
	if err == io.EOF && n == len(p) {
		err = nil
	}
	return n, err
}

func (c *Content) Bytes() []byte {
	b := make([]byte, c.Size)
	if c.Size > 0 {
		c.ReadAt(b, 0)
	}
	return b
}

func (c *Content) SHA256() string {
	h := sha256.New()
	io.Copy(h, c.NewReader())
	return hex.EncodeToString(h.Sum(nil))
}
