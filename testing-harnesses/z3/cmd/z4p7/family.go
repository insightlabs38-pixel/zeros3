package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"zeros3-z3/numcore"
)

func init() {
	cmds["family"] = cmdFamily
	cmds["replay"] = cmdReplay
	cmds["ranges"] = cmdRanges
}

type stateOut struct {
	File      string   `json:"file"`
	SHA       string   `json:"sha256"`
	Size      int64    `json:"size"`
	Object    int64    `json:"object"`  // ZN7 bytes (0 = identical to an earlier state)
	Charged   int64    `json:"charged"` // object + 96 B record + 36 B root reference
	Bases     []string `json:"bases"`
	Aux       []string `json:"aux,omitempty"`
	EncSecs   float64  `json:"enc_secs"`
	MaxDepth  int      `json:"max_depth"`
	ModeBytes [5]int64 `json:"mode_bytes"` // raw, direct, pred, copy, quant
	ModeCount [5]int64 `json:"mode_count"`
	Gap       int64    `json:"gap_bytes"`
	Dir       int64    `json:"dir_bytes"`
	Narrowed  int64    `json:"narrowed_windows"`
	Struct    int      `json:"structured_tensors"`
	QExact    int64    `json:"quant_exact_blocks"`
	QPatch    int64    `json:"quant_patched_blocks"`
	CandEvals int64    `json:"cand_evals"`
	FullEvals int64    `json:"full_evals"`
}

// storeRV resolves dependencies from a directory of ZN7 objects (<sha>.zn7) and aux blobs.
type storeRV struct {
	dir   string
	files []*os.File
}

func (s *storeRV) Open(ctx context.Context, id numcore.ID) (numcore.Stored, error) {
	f, err := os.Open(filepath.Join(s.dir, hex.EncodeToString(id[:])+".zn7"))
	if err != nil {
		return numcore.Stored{}, numcore.ErrMissing7
	}
	fi, err := f.Stat()
	if err != nil {
		return numcore.Stored{}, err
	}
	s.files = append(s.files, f)
	o, err := numcore.OpenObject(f, fi.Size())
	return numcore.Stored{Obj: o}, err
}

func (s *storeRV) Aux(ctx context.Context, id numcore.ID) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, hex.EncodeToString(id[:])+".aux"))
	if err != nil {
		return nil, numcore.ErrMissing7
	}
	return b, nil
}

func openStored(dir, sha string) (*numcore.Object, *os.File, error) {
	f, err := os.Open(filepath.Join(dir, sha+".zn7"))
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	o, err := numcore.OpenObject(f, fi.Size())
	return o, f, err
}

// family -store DIR -policy direct|first|prev|pool:K [flags] FILE...
// Encodes each file against earlier states (candidates by tensor name, optional HF-name mapping
// and content discovery), writes DIR/<sha>.zn7 and DIR/manifest.json.
func cmdFamily(args []string) {
	fl := flag.NewFlagSet("family", flag.ExitOnError)
	storeDir := fl.String("store", "", "store directory")
	policy := fl.String("policy", "pool:3", "direct|first|prev|pool:K")
	parents := fl.String("parents", "", "explicit parent states per file, e.g. '-;0;0,1' (overrides policy)")
	window := fl.Int64("window", 1<<16, "float elements per window")
	workers := fl.Int("workers", 4, "window coders")
	structured := fl.Bool("structured", false, "probe for exact affine/low-rank structure")
	exhaustive := fl.Bool("exhaustive", false, "oracle: code every candidate in full")
	disc := fl.Bool("disc", false, "content discovery: any same-size float tensor of a pooled base is a candidate")
	hfmap := fl.Bool("hfmap", false, "GGUF target vs Safetensors base: map tensor names (llama-style), try the head permutation")
	auxP := fl.String("aux", "", "comma-separated imatrix files usable for quant derivations")
	noNarrow := fl.Bool("nonarrow", false, "disable exact F32 narrowing")
	noVerify := fl.Bool("noverify", false, "skip encoder self-verification")
	jsonOut := fl.String("json", "", "write the result JSON here")
	fl.Parse(args)
	must(os.MkdirAll(*storeDir, 0o755))
	var aux []numcore.AuxIn
	for _, p := range strings.Split(*auxP, ",") {
		if p == "" {
			continue
		}
		b, err := os.ReadFile(p)
		must(err)
		aux = append(aux, numcore.AuxIn{ID: sha256.Sum256(b), Data: b})
	}
	var par [][]int
	if *parents != "" {
		for _, s := range strings.Split(*parents, ";") {
			var l []int
			for _, x := range strings.Split(s, ",") {
				if v, err := strconv.Atoi(x); err == nil {
					l = append(l, v)
				}
			}
			par = append(par, l)
		}
	}
	type state struct {
		m     *mfile
		depth []int
		gapD  int
		out   stateOut
	}
	var states []*state
	seen := map[numcore.ID]int{}
	var total int64
	auxStored := map[numcore.ID]bool{}
	for si, p := range fl.Args() {
		m, err := openModel(p, true)
		must(err)
		s := &state{m: m}
		s.out = stateOut{File: filepath.Base(p), SHA: hex.EncodeToString(m.id[:]), Size: m.lay.Size}
		states = append(states, s)
		if d, dup := seen[m.id]; dup {
			s.out.Charged = 36
			s.out.Bases = []string{states[d].out.SHA}
			s.depth, s.gapD = states[d].depth, states[d].gapD
			total += 36
			fmt.Printf("%-34s raw %11d identical to %s charged 36\n", s.out.File, m.lay.Size, states[d].out.File)
			continue
		}
		seen[m.id] = si
		// base pool
		var pool []int
		switch {
		case par != nil:
			if si < len(par) {
				pool = par[si]
			}
		case *policy == "direct":
		case *policy == "first":
			if si > 0 {
				pool = []int{0}
			}
		case *policy == "prev":
			if si > 0 {
				pool = []int{si - 1}
			}
		case strings.HasPrefix(*policy, "pool:"):
			k, _ := strconv.Atoi((*policy)[5:])
			for j := si - 1; j >= 0 && len(pool) < k; j-- {
				if states[j].out.Object > 0 {
					pool = append(pool, j)
				}
			}
		default:
			must(fmt.Errorf("unknown policy %q", *policy))
		}
		var bases []numcore.BaseIn
		for _, j := range pool {
			b := states[j]
			bases = append(bases, numcore.BaseIn{ID: b.m.id, Len: b.m.lay.Size, Src: b.m.src(), GapDepth: b.gapD,
				Depth: func(ti int) int { return b.depth[ti] }})
		}
		bySize := make([]map[int64][]int, len(pool))
		if *disc {
			for bi, j := range pool {
				bySize[bi] = map[int64][]int{}
				for ti, t := range states[j].m.lay.T {
					if t.DT.IsFloat() {
						bySize[bi][t.Len] = append(bySize[bi][t.Len], ti)
					}
				}
			}
		}
		cands := func(ti int) []numcore.Cand {
			t := &m.lay.T[ti]
			var out []numcore.Cand
			for bi, j := range pool {
				b := states[j].m
				if k, ok := b.byName[t.Name]; ok {
					out = append(out, numcore.Cand{Slot: bi, TIdx: k})
				}
				if *hfmap && m.format == "gguf" && b.format == "st" {
					if k, ok := b.byName[hfOf(t.Name)]; ok {
						out = append(out, numcore.Cand{Slot: bi, TIdx: k})
						h := 0
						if strings.Contains(t.Name, "attn_q.") {
							h = m.heads[0]
						} else if strings.Contains(t.Name, "attn_k.") {
							h = m.heads[1]
						}
						if h > 0 {
							out = append(out, numcore.Cand{Slot: bi, TIdx: k, Perm: numcore.PermHint{Kind: 1, Heads: uint16(h)}})
						}
					}
				}
				if *disc && t.DT.IsFloat() {
					for _, k := range bySize[bi][t.Len] {
						if b.lay.T[k].Name != t.Name {
							out = append(out, numcore.Cand{Slot: bi, TIdx: k})
						}
					}
				}
			}
			return out
		}
		tmp := filepath.Join(*storeDir, s.out.SHA+".tmp")
		f, err := os.Create(tmp)
		must(err)
		t0 := time.Now()
		st, err := numcore.Encode(context.Background(), f, m.f, m.lay, bases, cands, aux, numcore.EncOpts{Window: *window, Workers: *workers,
			Structured: *structured, Exhaustive: *exhaustive, NoNarrow: *noNarrow, NoVerify: *noVerify})
		must(err)
		must(f.Close())
		must(os.Rename(tmp, filepath.Join(*storeDir, s.out.SHA+".zn7")))
		o, of, err := openStored(*storeDir, s.out.SHA)
		must(err)
		if o.FileSHA != m.id {
			must(fmt.Errorf("%s: encoder digest differs from file digest", p))
		}
		used := map[int]bool{}
		for ti := range o.T {
			if o.T[ti].Ref != nil {
				used[int(o.T[ti].Ref.Slot)] = true
			}
		}
		if o.GapMode == 1 {
			used[int(o.GapSlot)] = true
		}
		for bi, j := range pool {
			if used[bi] {
				s.out.Bases = append(s.out.Bases, states[j].out.SHA)
			}
		}
		for _, a := range o.Aux {
			s.out.Aux = append(s.out.Aux, hex.EncodeToString(a[:]))
			if !auxStored[a] {
				for _, ai := range aux {
					if ai.ID == a {
						must(os.WriteFile(filepath.Join(*storeDir, hex.EncodeToString(a[:])+".aux"), ai.Data, 0o644))
						auxStored[a] = true
						total += int64(len(ai.Data)) + 132
					}
				}
			}
		}
		of.Close()
		s.depth, s.gapD = st.Depth, st.GapDepth
		for _, d := range st.Depth {
			if d > s.out.MaxDepth {
				s.out.MaxDepth = d
			}
		}
		s.out.Object, s.out.Charged, s.out.EncSecs = st.Bytes, st.Bytes+132, time.Since(t0).Seconds()
		s.out.ModeBytes, s.out.ModeCount, s.out.Gap, s.out.Dir = st.ModeBytes, st.ModeCount, st.GapBytes, st.DirBytes
		s.out.Narrowed, s.out.Struct, s.out.QExact, s.out.QPatch, s.out.CandEvals, s.out.FullEvals = st.Narrowed, st.Structured, st.QuantExact, st.QuantPatch, st.CandEvals, st.FullEvals
		total += s.out.Charged
		fmt.Printf("%-34s raw %11d object %11d (%.4f) %6.1fs %6.1f MiB/s depth %d modes raw/direct/pred/copy/quant %v struct %d qpatch %d/%d\n", s.out.File, m.lay.Size, st.Bytes,
			float64(st.Bytes)/float64(m.lay.Size), s.out.EncSecs, float64(m.lay.Size)/1048576/s.out.EncSecs, s.out.MaxDepth, st.ModeBytes, st.Structured, st.QuantPatch, st.QuantPatch+st.QuantExact)
	}
	outs := make([]stateOut, len(states))
	for i, s := range states {
		outs[i] = s.out
	}
	mb, _ := json.MarshalIndent(outs, "", " ")
	must(os.WriteFile(filepath.Join(*storeDir, "manifest.json"), mb, 0o644))
	fmt.Printf("TOTAL policy=%s charged=%d\n", *policy, total)
	if *jsonOut != "" {
		jb, _ := json.Marshal(map[string]any{"policy": *policy, "charged": total, "states": outs})
		must(os.WriteFile(*jsonOut, jb, 0o644))
	}
	printRSS("family")
}

func readManifest(dir string) []stateOut {
	mb, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	must(err)
	var man []stateOut
	must(json.Unmarshal(mb, &man))
	return man
}

type hashW struct {
	f *os.File
	h interface{ Write([]byte) (int, error) }
}

// replay -store DIR -out DIR [-workers N] [-cache BYTES]: fresh-process proof. Decodes every
// manifest state from the store alone into OUT/<sha256>; an external tool hashes the outputs.
func cmdReplay(args []string) {
	fl := flag.NewFlagSet("replay", flag.ExitOnError)
	dir := fl.String("store", "", "store")
	out := fl.String("out", "", "output dir ('' = discard)")
	workers := fl.Int("workers", 1, "window decoders")
	cache := fl.Int64("cache", 64<<20, "decoded base-window cache bytes")
	fl.Parse(args)
	if *out != "" {
		must(os.MkdirAll(*out, 0o755))
	}
	for _, e := range readManifest(*dir) {
		o, f, err := openStored(*dir, e.SHA)
		must(err)
		var w *os.File
		if *out != "" {
			w, err = os.Create(filepath.Join(*out, e.SHA))
		} else {
			w, err = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		}
		must(err)
		rv := &storeRV{dir: *dir}
		t0 := time.Now()
		st, err := o.DecodeTo(context.Background(), w, rv, numcore.DecOpts{Workers: *workers, CacheBytes: *cache})
		must(err)
		must(w.Close())
		ms := time.Since(t0).Milliseconds()
		fmt.Printf("{\"sha256\":%q,\"bytes\":%d,\"ms\":%d,\"mib_s\":%.1f,\"windows\":%d,\"store_read\":%d,\"cache_hits\":%d}\n", e.SHA, o.FileLen, ms,
			float64(o.FileLen)/1048576/(float64(ms)/1000+1e-9), st.Windows, st.PayloadBytes+st.DirBytes, st.CacheHits)
		f.Close()
		for _, x := range rv.files {
			x.Close()
		}
	}
	printRSS("replay")
}

// ranges -store DIR -orig FILE [-n 60]: random range reads of the stored form of FILE, each
// compared with the original bytes; reports store bytes read per request size.
func cmdRanges(args []string) {
	fl := flag.NewFlagSet("ranges", flag.ExitOnError)
	dir := fl.String("store", "", "store")
	orig := fl.String("orig", "", "original file")
	n := fl.Int("n", 60, "requests per size")
	fl.Parse(args)
	m, err := openModel(*orig, true)
	must(err)
	sha := hex.EncodeToString(m.id[:])
	r := rand.New(rand.NewSource(7))
	for _, sz := range []int64{4 << 10, 64 << 10, 1 << 20} {
		var read, raw, ms, wins int64
		for i := 0; i < *n; i++ {
			o, f, err := openStored(*dir, sha) // cold: a fresh object and session per request
			must(err)
			off := r.Int63n(m.lay.Size - sz)
			rv := &storeRV{dir: *dir}
			t0 := time.Now()
			got, st, err := o.ReadRange(context.Background(), off, sz, rv, numcore.DecOpts{})
			must(err)
			ms += time.Since(t0).Microseconds()
			want := make([]byte, sz)
			_, err = m.f.ReadAt(want, off)
			must(err)
			if !bytes.Equal(got, want) {
				must(fmt.Errorf("range %d+%d mismatch", off, sz))
			}
			read += st.PayloadBytes + st.DirBytes
			raw += st.RawBaseBytes
			wins += st.Windows
			f.Close()
			for _, x := range rv.files {
				x.Close()
			}
		}
		q := int64(*n)
		fmt.Printf("{\"request\":%d,\"store_read_avg\":%d,\"amplification\":%.1f,\"windows_avg\":%.1f,\"us_avg\":%d}\n", sz, read/q, float64(read/q)/float64(sz), float64(wins)/float64(q), ms/q)
	}
}
