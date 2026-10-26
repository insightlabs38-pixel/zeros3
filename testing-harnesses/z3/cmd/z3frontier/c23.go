package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"zeros3-z3/frontier"
	"zeros3-z3/lab"
	"zeros3-z3/tarz3"
)

type tools struct{ cprod, zstd, nydus string }

func findTools(root string) tools {
	return tools{cprod: filepath.Join(root, "z3cprod"), zstd: "zstd", nydus: filepath.Join(root, "nydus/nydus-static/nydus-image")}
}

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %v: %w\n%s", name, args, err, errb.String())
	}
	return out.String(), nil
}

func fileSHA(p string) string {
	b, err := os.ReadFile(p)
	must(err)
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h)
}

type rec map[string]any

const (
	tinyPath  = "tiny/d05/s03/f0012.go"
	largePath = "big/large.dat"
	largeOff  = 16<<20 + 4096*3 // 4 KiB probe inside the 17th 1 MiB chunk
)

// cover returns, for a set of native byte ranges, the physical bytes asked for and
// the bytes of production-CDC chunks (over the same blob) that overlap them.
func cover(b []byte, ranges []frontier.Span) rec {
	var bounds []int64
	must(lab.ChunkCDC(bytes.NewReader(b), func(o int64, ch []byte) error { bounds = append(bounds, o, int64(len(ch))); return nil }))
	var native, cdc int64
	for _, r := range ranges {
		native += r.Len
		for i := 0; i < len(bounds); i += 2 {
			if bounds[i] < r.Off+r.Len && r.Off < bounds[i]+bounds[i+1] {
				cdc += bounds[i+1]
			}
		}
	}
	return rec{"native": native, "regions": len(ranges), "cdc_cover": cdc}
}

// span returns the entry range [Off, Len) containing chunk index of file in a TOC chunk list.
func findChunk(cs []frontier.NativeChunk, name string, fileOff int64) (frontier.NativeChunk, bool) {
	for _, c := range cs {
		if c.Name == name && c.FileOff <= fileOff && fileOff < c.FileOff+c.ULen {
			return c, true
		}
	}
	return frontier.NativeChunk{}, false
}

// c23lane holds the stateful accounting of one container format across the chain.
type c23lane struct {
	name   string
	cdc    *frontier.Chain
	aux    map[string]*frontier.Chain
	blocks *frontier.BlockSet
	seen   map[string]bool // string-keyed native identity (chunk digests)
}

func newLane(n string) *c23lane {
	return &c23lane{name: n, cdc: frontier.NewChain(), aux: map[string]*frontier.Chain{}, blocks: frontier.NewBlockSet(), seen: map[string]bool{}}
}

func (l *c23lane) auxChain(n string) *frontier.Chain {
	if l.aux[n] == nil {
		l.aux[n] = frontier.NewChain()
	}
	return l.aux[n]
}

func spanIDs(b []byte, spans []frontier.Span) (ids [][32]byte, sizes []int64) {
	for _, s := range spans {
		ids = append(ids, sha256.Sum256(b[s.Off:s.Off+s.Len]))
		sizes = append(sizes, s.Len)
	}
	return
}

// chunkReuse accounts native chunks by content digest: a chunk costs its compressed
// span the first time its digest is seen. Returns count, new count, new compressed bytes.
func (l *c23lane) chunkReuse(cs []frontier.NativeChunk) (n, nnew int, newBytes, uBytes, uNew int64) {
	seenHere := map[string]bool{}
	for _, c := range cs {
		n++
		uBytes += c.ULen
		if l.seen[c.Digest] || seenHere[c.Digest] {
			continue
		}
		seenHere[c.Digest] = true
		nnew++
		newBytes += c.Len
		uNew += c.ULen
	}
	for d := range seenHere {
		l.seen[d] = true
	}
	return
}

func cmdC23(args []string) {
	fs := flag.NewFlagSet("c23", flag.ExitOnError)
	work := fs.String("work", "/root/z3scratch/c23", "scratch directory")
	out := fs.String("out", "/root/z3scratch/c23.jsonl", "results JSONL")
	tl := fs.String("tools", "/root/z3cache", "external tools root")
	revs := fs.String("revs", strings.Join(frontier.Revisions, ","), "revisions")
	fs.Parse(args)
	T := findTools(*tl)
	must(os.MkdirAll(*work, 0o755))
	of, err := os.Create(*out)
	must(err)
	defer of.Close()
	var omu sync.Mutex
	emit := func(r rec) {
		omu.Lock()
		defer omu.Unlock()
		b, _ := json.Marshal(r)
		of.Write(append(b, '\n'))
	}
	lanes := map[string]*c23lane{}
	for _, n := range []string{"tar", "targz", "tarzst", "estargz", "zstdchunked", "nydus"} {
		lanes[n] = newLane(n)
	}
	floor := frontier.NewFloor()
	base := frontier.Base()
	treeDir := filepath.Join(*work, "tree")
	for _, rev := range strings.Split(*revs, ",") {
		t0 := time.Now()
		tree := frontier.Revision(base, rev)
		must(frontier.Materialize(tree, treeDir))
		fc, err := floor.Add(tree, treeDir)
		must(err)
		emit(rec{"rev": rev, "lane": "floor", "files": fc.Files, "file_raw": fc.FileRaw, "file_packed": fc.FilePacked, "manifest": fc.Manifest, "manifest_raw": fc.ManifestRaw, "total": fc.Total, "obj_new": fc.ObjNew, "obj_bytes": fc.ObjBytes})
		tarPath := filepath.Join(*work, "r.tar")
		f, err := os.Create(tarPath)
		must(err)
		must(frontier.WriteTar(f, tree, treeDir))
		must(f.Close())
		var wg sync.WaitGroup
		lane := func(name string, fn func(l *c23lane) rec) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := fn(lanes[name])
				r["rev"], r["lane"] = rev, name
				emit(r)
			}()
		}
		lane("tar", func(l *c23lane) rec {
			b, err := os.ReadFile(tarPath)
			must(err)
			c, err := l.cdc.AddStream(bytes.NewReader(b))
			must(err)
			a, err := tarz3.Parse(b, tarz3.Options{})
			must(err)
			st, _ := tarz3.Split(b, a)
			sc, err := l.auxChain("struct").AddStream(bytes.NewReader(st))
			must(err)
			r := rec{"size": len(b), "cdc": c, "struct": sc, "struct_bytes": len(st), "members": len(a.Members)}
			if rev == "R0" {
				var tiny, large frontier.Span
				for _, m := range a.Members {
					if m.Path == tinyPath {
						tiny = frontier.Span{Off: m.Off, Len: m.End() - m.Off}
					}
					if m.Path == largePath {
						large = frontier.Span{Off: m.PayOff + largeOff, Len: 4096}
					}
				}
				r["read_tiny"], r["read_large"] = cover(b, []frontier.Span{tiny}), cover(b, []frontier.Span{large})
			}
			return r
		})
		lane("targz", func(l *c23lane) rec {
			p := filepath.Join(*work, "r.tar.gz")
			cmd := exec.Command("sh", "-c", "gzip -6 -n -c "+tarPath+" > "+p)
			must(cmd.Run())
			c, err := l.cdc.AddFile(p)
			must(err)
			st, _ := os.Stat(p)
			os.Remove(p)
			return rec{"size": st.Size(), "cdc": c}
		})
		lane("tarzst", func(l *c23lane) rec {
			p := filepath.Join(*work, "r.tar.zst")
			_, err := run(T.zstd, "-3", "-T1", "-q", "-f", tarPath, "-o", p)
			must(err)
			b, err := os.ReadFile(p)
			must(err)
			c, err := l.cdc.AddStream(bytes.NewReader(b))
			must(err)
			fr, err := frontier.ScanZstd(bytes.NewReader(b), int64(len(b)), 1<<16, 1<<20)
			must(err)
			var spans []frontier.Span
			nb := 0
			for _, f := range fr {
				for _, bl := range f.Blocks {
					spans = append(spans, frontier.Span{Off: bl.Off, Len: bl.Len})
					nb++
				}
			}
			ids, sz := spanIDs(b, spans)
			bc := l.blocks.Add(ids, sz)
			r := rec{"size": len(b), "cdc": c, "frames": len(fr), "blocks": nb, "blockset": bc}
			if rev == "R0" {
				// single frame: reaching a member means decoding every block before it. The tar offset
				// maps to block index off/128KiB (zstd streaming emits 128 KiB regenerated blocks; approximate).
				tb, _ := os.ReadFile(tarPath)
				a, _ := tarz3.Parse(tb, tarz3.Options{})
				prefix := func(tarEnd int64) frontier.Span {
					i := int(tarEnd / 131072)
					if i >= len(spans) {
						i = len(spans) - 1
					}
					return frontier.Span{Off: 0, Len: spans[i].Off + spans[i].Len}
				}
				for _, m := range a.Members {
					if m.Path == tinyPath {
						r["read_tiny"] = cover(b, []frontier.Span{prefix(m.End())})
					}
					if m.Path == largePath {
						r["read_large"] = cover(b, []frontier.Span{prefix(m.PayOff + largeOff + 4096)})
					}
				}
			}
			if rev == "R0" { // reproducibility: second run, same bytes?
				_, err = run(T.zstd, "-3", "-T1", "-q", "-f", tarPath, "-o", p+"2")
				must(err)
				r["repro"] = fileSHA(p) == fileSHA(p+"2")
				os.Remove(p + "2")
			}
			os.Remove(p)
			return r
		})
		lane("estargz", func(l *c23lane) rec {
			p := filepath.Join(*work, "r.estargz")
			js, err := run(T.cprod, "estargz", "-in", tarPath, "-out", p, "-chunk", "1048576")
			must(err)
			var meta map[string]any
			must(json.Unmarshal([]byte(js), &meta))
			b, err := os.ReadFile(p)
			must(err)
			tocJSON, err := os.ReadFile(p + ".toc.json")
			must(err)
			c, err := l.cdc.AddStream(bytes.NewReader(b))
			must(err)
			tocOff := int64(meta["toc_offset"].(float64))
			spans, err := frontier.GzipMembers(b, 1<<22)
			must(err)
			ids, sz := spanIDs(b, spans)
			mc := l.blocks.Add(ids, sz)
			toc, err := frontier.ParseTOC(tocJSON, 1<<22)
			must(err)
			n, nn, nb, ub, un := l.chunkReuse(toc.Chunks(tocOff))
			tc, err := l.auxChain("toc").AddStream(bytes.NewReader(b[tocOff:]))
			must(err)
			r := rec{"size": len(b), "cdc": c, "members": len(spans), "memberset": mc, "chunks": n, "chunks_new": nn,
				"chunk_new_bytes": nb, "chunk_u_bytes": ub, "chunk_u_new": un, "toc_bytes": int64(len(b)) - tocOff, "toc_json": len(tocJSON), "toc_cdc": tc}
			if rev == "R0" {
				cs := toc.Chunks(tocOff)
				ft := frontier.Span{Off: int64(len(b)) - int64(meta["footer"].(float64)), Len: int64(meta["footer"].(float64))}
				tc := frontier.Span{Off: tocOff, Len: int64(len(b)) - tocOff - ft.Len}
				if c, ok := findChunk(cs, tinyPath, 0); ok {
					r["read_tiny"] = cover(b, []frontier.Span{ft, tc, {Off: c.Off, Len: c.Len}})
				}
				if c, ok := findChunk(cs, largePath, largeOff); ok {
					r["read_large"] = cover(b, []frontier.Span{ft, tc, {Off: c.Off, Len: c.Len}})
				}
				_, err = run(T.cprod, "estargz", "-in", tarPath, "-out", p+"2", "-chunk", "1048576")
				must(err)
				r["repro"] = fileSHA(p) == fileSHA(p+"2")
				os.Remove(p + "2")
				os.Remove(p + "2.toc.json")
			}
			os.Remove(p)
			os.Remove(p + ".toc.json")
			return r
		})
		lane("zstdchunked", func(l *c23lane) rec {
			p := filepath.Join(*work, "r.zstdchunked")
			js, err := run(T.cprod, "zstdchunked", "-in", tarPath, "-out", p)
			must(err)
			var meta map[string]any
			must(json.Unmarshal([]byte(js), &meta))
			b, err := os.ReadFile(p)
			must(err)
			tocJSON, err := os.ReadFile(p + ".toc.json")
			must(err)
			c, err := l.cdc.AddStream(bytes.NewReader(b))
			must(err)
			mOff := int64(meta["manifest_offset"].(float64))
			fr, err := frontier.ScanZstd(bytes.NewReader(b), int64(len(b)), 1<<22, 1<<24)
			must(err)
			var spans []frontier.Span
			for _, f := range fr {
				if !f.Skippable {
					spans = append(spans, frontier.Span{Off: f.Off, Len: f.Len})
				}
			}
			ids, sz := spanIDs(b, spans)
			fc := l.blocks.Add(ids, sz)
			toc, err := frontier.ParseTOC(tocJSON, 1<<22)
			must(err)
			n, nn, nb, ub, un := l.chunkReuse(toc.Chunks(mOff))
			tc, err := l.auxChain("toc").AddStream(bytes.NewReader(b[mOff:]))
			must(err)
			r := rec{"size": len(b), "cdc": c, "frames": len(fr), "frameset": fc, "chunks": n, "chunks_new": nn,
				"chunk_new_bytes": nb, "chunk_u_bytes": ub, "chunk_u_new": un, "toc_bytes": int64(len(b)) - mOff, "toc_json": len(tocJSON), "toc_cdc": tc}
			if rev == "R0" {
				cs := toc.Chunks(mOff)
				ft := frontier.Span{Off: int64(len(b)) - 72, Len: 72} // skippable header (8) + footer (64)
				tc := frontier.Span{Off: mOff, Len: int64(meta["manifest_clen"].(float64))}
				if c, ok := findChunk(cs, tinyPath, 0); ok {
					r["read_tiny"] = cover(b, []frontier.Span{ft, tc, {Off: c.Off, Len: c.Len}})
				}
				// a 4 KiB probe lies in exactly one content-defined chunk
				if c, ok := findChunk(cs, largePath, largeOff); ok {
					r["read_large"] = cover(b, []frontier.Span{ft, tc, {Off: c.Off, Len: c.Len}})
					r["read_large_chunk_u"] = c.ULen
				}
				_, err = run(T.cprod, "zstdchunked", "-in", tarPath, "-out", p+"2")
				must(err)
				r["repro"] = fileSHA(p) == fileSHA(p+"2")
				os.Remove(p + "2")
				os.Remove(p + "2.toc.json")
			}
			os.Remove(p)
			os.Remove(p + ".toc.json")
			return r
		})
		lane("nydus", func(l *c23lane) rec {
			nd := filepath.Join(*work, "nydus")
			os.RemoveAll(nd)
			must(os.MkdirAll(filepath.Join(nd, "blobs"), 0o755))
			build := func(dir string) {
				os.MkdirAll(filepath.Join(dir, "blobs"), 0o755)
				_, err := run(T.nydus, "create", "-t", "dir-rafs", "--fs-version", "6", "--compressor", "zstd", "--chunk-size", "0x100000",
					"--digester", "sha256", "--blob-dir", filepath.Join(dir, "blobs"), "--bootstrap", filepath.Join(dir, "boot"), treeDir)
				must(err)
			}
			build(nd)
			ents, _ := os.ReadDir(filepath.Join(nd, "blobs"))
			must(nil)
			blob := filepath.Join(nd, "blobs", ents[0].Name())
			bb, err := os.ReadFile(blob)
			must(err)
			boot, err := os.ReadFile(filepath.Join(nd, "boot"))
			must(err)
			c, err := l.cdc.AddStream(bytes.NewReader(bb))
			must(err)
			bc, err := l.auxChain("boot").AddStream(bytes.NewReader(boot))
			must(err)
			chk, err := run(T.nydus, "check", "-B", filepath.Join(nd, "boot"), "-v")
			must(err)
			cs, refs := parseNydusChunks(chk)
			n, nn, nbytes, ub, un := l.chunkReuse(cs)
			r := rec{"size": len(bb), "boot": len(boot), "cdc": c, "boot_cdc": bc, "chunks": n, "chunks_new": nn, "chunk_new_bytes": nbytes, "chunk_u_bytes": ub, "chunk_u_new": un}
			if rev == "R0" {
				bs := frontier.Span{Off: 0, Len: int64(len(boot))}
				if c, ok := findChunk(refs, tinyPath, 0); ok {
					rr := cover(bb, []frontier.Span{{Off: c.Off, Len: c.Len}})
					rr["boot_total"], rr["boot_est"] = bs.Len, 3*4096 // bootstrap is random-access; ~3 metadata blocks (estimate)
					r["read_tiny"] = rr
				}
				if c, ok := findChunk(refs, largePath, largeOff); ok {
					rr := cover(bb, []frontier.Span{{Off: c.Off, Len: c.Len}})
					rr["boot_total"], rr["boot_est"] = bs.Len, 4*4096
					r["read_large"] = rr
				}
				nd2 := filepath.Join(*work, "nydus2")
				os.RemoveAll(nd2)
				build(nd2)
				e2, _ := os.ReadDir(filepath.Join(nd2, "blobs"))
				r["repro"] = e2[0].Name() == ents[0].Name() && fileSHA(filepath.Join(nd2, "boot")) == fileSHA(filepath.Join(nd, "boot"))
				os.RemoveAll(nd2)
			}
			os.RemoveAll(nd)
			return r
		})
		wg.Wait()
		fmt.Fprintf(os.Stderr, "c23 %s done in %v\n", rev, time.Since(t0).Round(time.Second))
	}
	os.RemoveAll(treeDir)
	os.Remove(filepath.Join(*work, "r.tar"))
}

var nydusChunkRE = regexp.MustCompile(`chunk: id ([0-9a-f]+), index (\d+), blob_index (\d+), file_offset (\d+), compressed (\d+)/(\d+), uncompressed (\d+)/(\d+)`)
var nydusInodeRE = regexp.MustCompile(`^inode: file "([^"]+)"`)

// parseNydusChunks reads `nydus-image check -v` output: one chunk line per chunk
// reference (shared chunks repeat); compressed spans are blob offset/length.
// It returns the stored chunks (deduplicated by blob position) and every
// (file, file offset) reference so point reads can find their chunk.
func parseNydusChunks(s string) (stored, refs []frontier.NativeChunk) {
	seen := map[string]bool{}
	cur := ""
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := nydusInodeRE.FindStringSubmatch(line); m != nil {
			cur = strings.TrimPrefix(m[1], "/")
			continue
		}
		m := nydusChunkRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		off, _ := strconv.ParseInt(m[5], 10, 64)
		cl, _ := strconv.ParseInt(m[6], 10, 64)
		ul, _ := strconv.ParseInt(m[8], 10, 64)
		fo, _ := strconv.ParseInt(m[4], 10, 64)
		c := frontier.NativeChunk{Name: cur, Off: off, Len: cl, ULen: ul, Digest: m[1], FileOff: fo}
		refs = append(refs, c)
		if key := m[1] + ":" + m[5]; !seen[key] { // a chunk shared by several files is stored once
			seen[key] = true
			stored = append(stored, c)
		}
	}
	return
}
