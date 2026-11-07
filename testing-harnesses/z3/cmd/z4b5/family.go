package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"zeros3-z3/mdelta2"
	"zeros3-z3/semcas"
	"zeros3-z3/simdelta"
)

type stateOut struct {
	File      string         `json:"file"`
	Raw       int64          `json:"raw"`
	Container int            `json:"container"`
	Charged   int64          `json:"charged"`
	Bases     []int          `json:"bases"`
	ModeBytes map[string]int `json:"mode_bytes"`
	EncSecs   float64        `json:"enc_secs"`
	Exact     bool           `json:"exact"`
	DecMS     int64          `json:"dec_ms"`
	NCopy     int            `json:"n_copy"`
	NDirect   int            `json:"n_direct"`
	NPred     int            `json:"n_pred"`
	MaxDepth  int            `json:"max_depth"`
}

// chargeContainer is the B1 accounting unit: production pack rule, 96 B record+locator, 36 B root reference.
func chargeContainer(b []byte) int64 { return int64(simdelta.Physical(b)) + 36 }

func parseInts(s string) []int {
	var out []int
	for _, p := range strings.Split(s, ",") {
		if p == "" {
			continue
		}
		v, err := strconv.Atoi(p)
		must(err)
		out = append(out, v)
	}
	return out
}

// sketch is a coarse numerical fingerprint used only to rank candidate bases.
func sketch(m *mdelta2.Model) []float64 {
	idx := make([]int, 0, len(m.Tensors))
	for i := range m.Tensors {
		if m.Tensors[i].DType.IsFloat() {
			idx = append(idx, i)
		}
	}
	sort.Slice(idx, func(a, b int) bool { return m.Tensors[idx[a]].Size() > m.Tensors[idx[b]].Size() })
	if len(idx) > 24 {
		idx = idx[:24]
	}
	var sk []float64
	for _, ti := range idx {
		t := &m.Tensors[ti]
		d, err := m.Read(ti)
		if err != nil {
			continue
		}
		w := t.DType.Width()
		n := len(d) / w
		base := mdelta2.Base{DT: t.DType, Data: d}
		for k := 0; k < 256; k++ {
			sk = append(sk, base.At(k*(n/256)))
		}
	}
	return sk
}

func sketchDist(a, b []float64) float64 {
	if len(a) != len(b) {
		return 1e18
	}
	var d, s float64
	for i := range a {
		x := a[i] - b[i]
		if x < 0 {
			x = -x
		}
		d += x
		y := a[i]
		if y < 0 {
			y = -y
		}
		s += y
	}
	return d / (s + 1e-30)
}

func cmdFamily(args []string) {
	fl := flag.NewFlagSet("family", flag.ExitOnError)
	policy := fl.String("policy", "direct", "direct|first|prev|sel2|lin2|pool3|pool3lin|oracle:<json parents>")
	storeDir := fl.String("store", "", "store directory (containers)")
	narrow := fl.Bool("narrow", true, "exact F32->F16/BF16 narrowing")
	aff := fl.Bool("aff", false, "affine predictors")
	low := fl.String("low", "", "low-rank ladder, e.g. 1,4,16")
	lin := fl.Bool("lin", false, "multi-base linear")
	seg := fl.Bool("seg", false, "per-segment base choice")
	rangeSeg := fl.Int("rangeseg", 0, "force independent segments of this many elements (range-read bound)")
	coders := fl.String("coders", "1,2,6", "coder ids")
	parents := fl.String("parents", "", "explicit parents per state, e.g. '-;0;0,1;1' (oracle/re-rooting)")
	jsonOut := fl.String("json", "", "write JSON result here")
	disc := fl.Bool("disc", false, "content-based candidate discovery (ignore names): best same-size tensor per base by numerical sketch")
	hfmap := fl.Bool("hfmap", false, "map GGUF tensor names to Safetensors names for float->quantized reproduction")
	heads := fl.String("heads", "0,0", "llama q/k permutation heads (n_head,n_kv); 0 = none")
	fl.Parse(args)
	opt := mdelta2.EncOpts{Narrow: *narrow, Affine: *aff, Low: parseInts(*low), Lin: *lin, Segments: *seg, Coders: parseInts(*coders), RangeSeg: *rangeSeg}
	hv := parseInts(*heads)
	opt.Hint = func(name string) mdelta2.PermHint {
		switch {
		case hv[0] > 0 && strings.Contains(name, "attn_q."):
			return mdelta2.PermHint{Kind: 1, Heads: hv[0]}
		case hv[1] > 0 && strings.Contains(name, "attn_k."):
			return mdelta2.PermHint{Kind: 1, Heads: hv[1]}
		}
		return mdelta2.PermHint{}
	}
	paths := fl.Args()
	store := mdelta2.StoreDir{Dir: *storeDir}
	var models []*mdelta2.Model
	var shas []mdelta2.SHA
	var sks [][]float64
	var outs []stateOut
	var depths [][]int
	var gdepths []int
	manifest := []map[string]any{}
	var total int64
	for i, p := range paths {
		m, err := mdelta2.Open(p)
		must(err)
		models = append(models, m)
		fb := make([]byte, m.Size)
		_, err = m.ReadAt(fb, 0)
		must(err)
		shas = append(shas, mdelta2.FileSHA(fb))
		fb = nil
		if *policy == "pool3" || *policy == "pool3lin" {
			sks = append(sks, sketch(m))
		}
		dupOf := -1
		for j := 0; j < i; j++ {
			if shas[j] == shas[i] {
				dupOf = j
				break
			}
		}
		if dupOf >= 0 { // byte-identical to an earlier state: content addressing stores nothing new
			depths = append(depths, depths[dupOf])
			gdepths = append(gdepths, gdepths[dupOf])
			so := stateOut{File: filepath.Base(p), Raw: m.Size, Charged: 36, Bases: []int{dupOf}, ModeBytes: map[string]int{"identical-state": 0}, Exact: true}
			outs = append(outs, so)
			total += so.Charged
			manifest = append(manifest, map[string]any{"path": p, "sha256": hex.EncodeToString(shas[i][:]), "size": m.Size})
			b, _ := json.Marshal(so)
			fmt.Println(string(b))
			continue
		}
		var parentIdx []int
		switch {
		case i == 0 || *policy == "direct":
		case *policy == "first":
			parentIdx = []int{0}
		case *policy == "prev":
			parentIdx = []int{i - 1}
		case *policy == "sel2" || *policy == "lin2":
			parentIdx = []int{i - 1}
			if i-1 != 0 {
				parentIdx = append(parentIdx, 0)
			}
		case *policy == "pool3" || *policy == "pool3lin":
			type sd struct {
				j int
				d float64
			}
			var l []sd
			for j := 0; j < i; j++ {
				l = append(l, sd{j, sketchDist(sks[i], sks[j])})
			}
			sort.Slice(l, func(a, b int) bool { return l[a].d < l[b].d })
			for k := 0; k < len(l) && k < 3; k++ {
				parentIdx = append(parentIdx, l[k].j)
			}
		case *policy == "explicit":
			ps := strings.Split(*parents, ";")
			if i < len(ps) && ps[i] != "-" {
				parentIdx = parseInts(ps[i])
			}
		default:
			must(fmt.Errorf("unknown policy %s", *policy))
		}
		var bases []mdelta2.BaseSrc
		for _, j := range parentIdx {
			bases = append(bases, mdelta2.BaseSrc{SHA: shas[j], Len: models[j].Size, M: models[j], TDepth: depths[j], GapDepth: gdepths[j]})
		}
		o := opt
		if *policy == "lin2" || *policy == "pool3lin" {
			o.Lin = true
		}
		var discPick map[int][]mdelta2.Pick // target tensor -> content-matched pick per base slot
		if *disc && len(bases) > 0 {
			discPick = map[int][]mdelta2.Pick{}
			var pools [][]*mdelta2.PT
			for s, b := range bases {
				var pl []*mdelta2.PT
				for j := range b.M.Tensors {
					if !b.M.Tensors[j].DType.IsFloat() {
						continue
					}
					d, _ := b.M.Read(j)
					pt := mdelta2.NewPTLite(s, j, &b.M.Tensors[j], d)
					pl = append(pl, pt)
				}
				pools = append(pools, pl)
			}
			for ti := range m.Tensors {
				if !m.Tensors[ti].DType.IsFloat() {
					continue
				}
				d, _ := m.Read(ti)
				tp := mdelta2.NewPTLite(-1, ti, &m.Tensors[ti], d)
				for s := range bases {
					bestS, bestJ := 0.0, -1
					for _, c := range pools[s] {
						if c.Elems != tp.Elems {
							continue
						}
						sc := mdelta2.Score("sha", tp, c)*10 + mdelta2.Score("combined", tp, c)
						if sc > bestS {
							bestS, bestJ = sc, c.Idx
						}
					}
					if bestJ >= 0 {
						discPick[ti] = append(discPick[ti], mdelta2.Pick{Slot: s, TIdx: bestJ})
					}
				}
			}
		}
		cands := func(ti int) [][]mdelta2.Pick {
			var out [][]mdelta2.Pick
			var singles []mdelta2.Pick
			if discPick != nil {
				singles = discPick[ti]
				for _, pk := range singles {
					out = append(out, []mdelta2.Pick{pk})
				}
				if o.Lin {
					for a := 0; a < len(singles); a++ {
						for b := a + 1; b < len(singles); b++ {
							out = append(out, []mdelta2.Pick{singles[a], singles[b]})
						}
					}
				}
				return out
			}
			for s, b := range bases {
				if j, ok := b.M.Index(m.Tensors[ti].Name); ok {
					singles = append(singles, mdelta2.Pick{Slot: s, TIdx: j})
				}
				if *hfmap && m.Format == "gguf" && b.M.Format == "st" {
					if j, ok := b.M.Index(hfOf(m.Tensors[ti].Name)); ok {
						singles = append(singles, mdelta2.Pick{Slot: s, TIdx: j})
					}
				}
			}
			for _, pk := range singles {
				out = append(out, []mdelta2.Pick{pk})
			}
			if o.Lin {
				for a := 0; a < len(singles); a++ {
					for b := a + 1; b < len(singles); b++ {
						out = append(out, []mdelta2.Pick{singles[a], singles[b]})
					}
				}
				if len(singles) == 3 {
					out = append(out, singles)
				}
			}
			return out
		}
		t0 := time.Now()
		cb, st, err := mdelta2.EncodeModel(m, bases, cands, o)
		must(err)
		depths = append(depths, st.TDepth)
		gdepths = append(gdepths, st.GapDepth)
		maxd := 0
		for _, d := range st.TDepth {
			if d > maxd {
				maxd = d
			}
		}
		// proof before accept: decode with the original base files and compare digests
		rv := mdelta2.ModelResolver{}
		for _, b := range bases {
			rv[b.SHA] = b.M
		}
		t1 := time.Now()
		got, err := mdelta2.Decode(cb, rv)
		exact := err == nil && mdelta2.FileSHA(got) == shas[i] && int64(len(got)) == m.Size
		dec := time.Since(t1)
		c, perr := mdelta2.ParseContainer(cb)
		must(perr)
		must(store.Put(c, cb))
		so := stateOut{File: filepath.Base(p), Raw: m.Size, Container: len(cb), Charged: chargeContainer(cb), Bases: parentIdx, ModeBytes: st.ModeBytes, MaxDepth: maxd, EncSecs: time.Since(t0).Seconds(), Exact: exact, DecMS: dec.Milliseconds()}
		for _, ts := range st.T {
			switch {
			case ts.Mode == "copy":
				so.NCopy++
			case ts.Mode == "direct":
				so.NDirect++
			default:
				so.NPred++
			}
		}
		total += so.Charged
		outs = append(outs, so)
		manifest = append(manifest, map[string]any{"path": p, "sha256": hex.EncodeToString(shas[i][:]), "size": m.Size})
		b, _ := json.Marshal(so)
		fmt.Println(string(b))
		if !exact {
			fmt.Fprintln(os.Stderr, "NOT EXACT:", p, err)
			os.Exit(1)
		}
	}
	mb, _ := json.MarshalIndent(manifest, "", " ")
	os.WriteFile(filepath.Join(*storeDir, "manifest.json"), mb, 0o644)
	fmt.Printf("TOTAL policy=%s charged=%d\n", *policy, total)
	printRSS("family")
	if *jsonOut != "" {
		jb, _ := json.MarshalIndent(map[string]any{"policy": *policy, "opts": os.Args, "states": outs, "total": total}, "", " ")
		os.WriteFile(*jsonOut, jb, 0o644)
	}
}

func cmdCDCBase(args []string) {
	prod, fine := semcas.ProdCDC(), semcas.FineCDC()
	var pt, ft, raw int64
	for _, f := range args {
		d, err := os.ReadFile(f)
		must(err)
		a, b := prod.Add(d), fine.Add(d)
		pt, ft, raw = prod.Total, fine.Total, raw+int64(len(d))
		fmt.Printf("{\"file\":%q,\"raw\":%d,\"prod_inc\":%d,\"prod_cum\":%d,\"fine_inc\":%d,\"fine_cum\":%d}\n", filepath.Base(f), len(d), a, pt, b, ft)
	}
	_ = bytes.MinRead
}

func cmdModel8(args []string) {
	// two bounded Z3-29 policies: greedy per revision (CDC allowed) and the fixed structural anchor|previous policy
	incG, totG, exG, err := model8Chain(args, false)
	must(err)
	incF, totF, exF, err := model8Chain(args, true)
	must(err)
	best, inc := totG, incG
	if totF < best {
		best, inc = totF, incF
	}
	b, _ := json.Marshal(map[string]any{"model8_inc": inc, "model8_total": best, "greedy_total": totG, "fixed_total": totF, "exact": exG && exF})
	fmt.Println(string(b))
}

// cmdReplay is the fresh-process proof: decode every manifest state from the store alone.
func cmdReplay(args []string) {
	fl := flag.NewFlagSet("replay", flag.ExitOnError)
	dir := fl.String("store", "", "store")
	out := fl.String("out", "", "output dir")
	fl.Parse(args)
	mb, err := os.ReadFile(filepath.Join(*dir, "manifest.json"))
	must(err)
	var man []struct {
		SHA  string `json:"sha256"`
		Size int64  `json:"size"`
	}
	must(json.Unmarshal(mb, &man))
	must(os.MkdirAll(*out, 0o755))
	s := mdelta2.StoreDir{Dir: *dir}
	for _, e := range man {
		raw, _ := hex.DecodeString(e.SHA)
		var id mdelta2.SHA
		copy(id[:], raw)
		t0 := time.Now()
		b, r, err := s.Replay(id)
		must(err)
		must(os.WriteFile(filepath.Join(*out, e.SHA), b, 0o644))
		fmt.Printf("{\"sha256\":%q,\"bytes\":%d,\"ms\":%d,\"store_read\":%d,\"loads\":%d}\n", e.SHA, len(b), time.Since(t0).Milliseconds(), r.Reads, r.Loads)
	}
	printRSS("replay")
}
