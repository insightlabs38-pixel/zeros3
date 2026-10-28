package frontiercapture

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"zeros3-z3/admission"
	"zeros3-z3/clrz3"
	"zeros3-z3/dexz3"
	"zeros3-z3/econx"
	"zeros3-z3/gitz3"
	"zeros3-z3/merklez3"
	"zeros3-z3/modelz3"
	"zeros3-z3/parquetz3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/sstz3"
)

type input struct{ label, path string }
type variant struct {
	name   string
	encode func([]byte) (*kinds.Bundle, error)
}
type builder struct{ c *admission.Capture }

func load(path string) []byte {
	b, e := os.ReadFile(path)
	if e != nil {
		panic(e)
	}
	if len(b) > 128<<20 {
		panic("input size bound")
	}
	return b
}
func (b *builder) add(name, class, evidence string, inputs []input, variants []variant) error {
	if len(inputs) == 0 {
		return fmt.Errorf("missing required case %s", name)
	}
	b.c.ResetStore()
	f := admission.Case{Name: name, Class: class, Evidence: evidence, KeyHint: name}
	for _, in := range inputs {
		data := load(in.path)
		b.c.Case, b.c.Stage, b.c.Class = name, in.label, class
		s := admission.Stage{Label: in.label, Size: int64(len(data)), InputSHA: fmt.Sprintf("%x", sha256.Sum256(data))}
		for _, v := range append([]variant{{"cdc", admission.CDC}}, variants...) {
			start := time.Now()
			bundle, e := v.encode(data)
			if e != nil {
				return fmt.Errorf("%s/%s/%s encode: %w", name, in.label, v.name, e)
			}
			p, e := b.c.Variant(v.name, data, bundle, start)
			if e != nil {
				return fmt.Errorf("%s/%s/%s proof: %w", name, in.label, v.name, e)
			}
			s.Candidates = append(s.Candidates, p)
		}
		f.Stages = append(f.Stages, s)
	}
	b.c.Corpus.Cases = append(b.c.Corpus.Cases, f)
	fmt.Fprintf(os.Stderr, "captured %s stages=%d\n", name, len(inputs))
	return nil
}
func Build(module, public, producer, git string, observe func(admission.Observation) error) (*admission.Corpus, error) {
	b := builder{admission.NewCapture()}
	b.c.Observe = observe
	var pub []struct {
		File, Class, SHA256 string
		Bytes               int64
		Source              struct{ Group, Name string }
	}
	if e := json.Unmarshal(load(filepath.Join(public, "provenance.json")), &pub); e != nil {
		return nil, e
	}
	groups := map[string][]input{}
	for _, r := range pub {
		if r.File != "" {
			if e := verifyPin(r.File, r.SHA256, r.Bytes); e != nil {
				return nil, e
			}
			groups[r.Source.Group] = append(groups[r.Source.Group], input{r.Source.Name, r.File})
		}
	}
	car := variant{"car", func(data []byte) (*kinds.Bundle, error) {
		b, _, e := kinds.EncodeCAR(bytes.NewReader(data), int64(len(data)))
		return b, e
	}}
	if e := b.add("car-public-repack", "car", "REAL", groups["car"], []variant{car}); e != nil {
		return nil, e
	}
	for _, kind := range []string{"fdroid-apk", "clr"} {
		var vs []variant
		for _, target := range []int{2048, 4096} {
			t := target
			v := variant{name: fmt.Sprintf("indexed-%d", t)}
			if kind == "clr" {
				v.encode = func(d []byte) (*kinds.Bundle, error) {
					b, _, e := kinds.EncodeCLR(d, clrz3.Config{Cut: econx.UnitCutBytes(t)})
					return b, e
				}
			} else {
				v.encode = func(d []byte) (*kinds.Bundle, error) {
					b, _, e := kinds.EncodeDEX(d, dexz3.Config{Cut: econx.UnitCutBytes(t)})
					return b, e
				}
			}
			vs = append(vs, v)
		}
		if e := b.add(kind+"-public", kind, "REAL", groups[kind], vs); e != nil {
			return nil, e
		}
	}
	// Model bases are generation-side candidates only. Their entire dependency
	// closure is priced by replay if an admission policy never stored the base.
	var bases []map[string]*modelz3.BaseTensor
	standalone := variant{"model-standalone", func(d []byte) (*kinds.Bundle, error) {
		src := &modelz3.Src{R: bytes.NewReader(d), Size: int64(len(d))}
		lay, _, e := modelz3.STLayout(src.R, src.Size)
		if e != nil {
			return nil, e
		}
		bundle, _, _, e := modelz3.EncodeModel(src, lay, nil, b.c.Store.Has, modelz3.Policy{Planes: true, MaxHops: 2})
		return bundle, e
	}}
	delta := variant{"model-delta", func(d []byte) (*kinds.Bundle, error) {
		src := &modelz3.Src{R: bytes.NewReader(d), Size: int64(len(d))}
		lay, _, e := modelz3.STLayout(src.R, src.Size)
		if e != nil {
			return nil, e
		}
		bundle, next, _, e := modelz3.EncodeModel(src, lay, bases, b.c.Store.Has, modelz3.Policy{Delta: true, Planes: true, MaxHops: 2})
		if e == nil {
			bases = append(bases, next)
		}
		return bundle, e
	}}
	if e := b.add("pythia14m-training", "model", "REAL", groups["pythia-14m"], []variant{standalone, delta}); e != nil {
		return nil, e
	}
	var prod []struct {
		Group, Label, File, Class, SHA256 string
		Bytes                             int64
	}
	if e := json.Unmarshal(load(filepath.Join(producer, "provenance.json")), &prod); e != nil {
		return nil, e
	}
	pg := map[string][]input{}
	for _, r := range prod {
		if e := verifyPin(r.File, r.SHA256, r.Bytes); e != nil {
			return nil, e
		}
		pg[r.Group] = append(pg[r.Group], input{r.Label, r.File})
	}
	sqlite := variant{"sqlite", func(d []byte) (*kinds.Bundle, error) {
		bundle, _, e := kinds.EncodeSQLite(bytes.NewReader(d), int64(len(d)))
		return bundle, e
	}}
	if e := b.add("sqlite-native", "sqlite", "PRODUCER_WORKLOAD", pg["sqlite-native"], []variant{sqlite}); e != nil {
		return nil, e
	}
	child := func(d []byte) (*kinds.Bundle, error) { b, _, e := kinds.EncodeTarLayer(d, 1<<20); return b, e }
	gzip := variant{"gzip-tar", func(d []byte) (*kinds.Bundle, error) { b, _, e := kinds.EncodeGzip(d, child); return b, e }}
	zstd := variant{"zstd-tar", func(d []byte) (*kinds.Bundle, error) { b, _, e := kinds.EncodeZstd(d, child); return b, e }}
	if e := b.add("oci-gzip-busybox", "gzip", "REAL", pg["oci-gzip"], []variant{gzip}); e != nil {
		return nil, e
	}
	if e := b.add("oci-zstd-busybox", "zstd", "REAL_REEXPORT", pg["oci-zstd"], []variant{zstd}); e != nil {
		return nil, e
	}
	for _, engine := range []string{"rocksdb", "pebble"} {
		var vs []variant
		for _, form := range []sstz3.Form{sstz3.FormBlocks, sstz3.FormUKV, sstz3.FormKVT} {
			form := form
			vs = append(vs, variant{form.String(), func(d []byte) (*kinds.Bundle, error) {
				bundle, _, e := encodeSnapshot(d, func(table []byte) (*kinds.Bundle, error) {
					var cut sstz3.Cutter
					if form != sstz3.FormBlocks {
						cut = &sstz3.HashCutter{Target: 8192, Max: 32768}
					}
					b, _, _, e := kinds.EncodeSST(bytes.NewReader(table), int64(len(table)), sstz3.Options{Form: form, Cutter: cut})
					return b, e
				})
				return bundle, e
			}})
		}
		if e := b.add(engine+"-native-state", engine, "PRODUCER_WORKLOAD", pg[engine], vs); e != nil {
			return nil, e
		}
	}
	var pq []input
	for _, name := range []string{"pa_snappy_v1.parquet", "pa_rg500.parquet", "pa_snappy_v2.parquet"} {
		pq = append(pq, input{name, filepath.Join(module, "parquetz3/testdata", name)})
	}
	var pqv []variant
	for _, t := range []int{4096, 8192} {
		t := t
		pqv = append(pqv, variant{fmt.Sprintf("parquet-%d", t), func(d []byte) (*kinds.Bundle, error) {
			b, _, _, e := kinds.EncodeParquet(bytes.NewReader(d), int64(len(d)), parquetz3.Options{Target: t})
			return b, e
		}})
	}
	if e := b.add("parquet-native-layouts", "parquet", "PRODUCER_WORKLOAD", pq, pqv); e != nil {
		return nil, e
	}
	var gp []input
	for _, name := range []string{"hiredis-full.pack", "hiredis-reordered.pack", "hiredis-delta.pack"} {
		gp = append(gp, input{name, filepath.Join(git, name)})
	}
	if e := b.add("git-hiredis-reexport", "git", "REAL_REEXPORT", gp, []variant{{"gp-zlib", func(d []byte) (*kinds.Bundle, error) { b, _, e := kinds.EncodeGit(d, gitz3.SHA1); return b, e }}}); e != nil {
		return nil, e
	}
	// A small existing DAG fixture supplies deep-root bootstrap controls; it is
	// explicitly synthetic and cannot stand in for a natural Ethereum history.
	ch, e := kinds.MerkleLongChain(merklez3.HashSHA3_256, 300, 5)
	if e != nil {
		return nil, e
	}
	b.c.ResetStore()
	cat := repz3.NewCatalog(b.c.Store, nil)
	cat.SetLimits(repz3.Limits{MaxDepth: 4})
	f := admission.Case{Name: "merkle-deep-control", Class: "merkle", Evidence: "SYNTHETIC_FIXTURE", KeyHint: "snapshot"}
	for _, stage := range ch.Stages {
		data := stage.Data
		b.c.Case, b.c.Stage, b.c.Class = f.Name, stage.Label, f.Class
		s := admission.Stage{Label: stage.Label, Size: int64(len(data)), InputSHA: fmt.Sprintf("%x", sha256.Sum256(data))}
		for _, v := range []variant{{"cdc", admission.CDC}, {"merkle-related", func(d []byte) (*kinds.Bundle, error) { return ch.Encode(b.c.Store, cat, d) }}} {
			start := time.Now()
			bundle, e := v.encode(data)
			if e != nil {
				return nil, e
			}
			p, e := b.c.Variant(v.name, data, bundle, start)
			if e != nil {
				return nil, e
			}
			s.Candidates = append(s.Candidates, p)
			if v.name != "cdc" {
				if e := cat.Ingest(bundle.ID); e != nil {
					return nil, e
				}
			}
		}
		f.Stages = append(f.Stages, s)
	}
	b.c.Corpus.Cases = append(b.c.Corpus.Cases, f)
	// Preregistered one-off and missing-hint controls reuse exact captured costs,
	// not regenerated bytes or optimistic synthetic future savings.
	original := append([]admission.Case(nil), b.c.Corpus.Cases...)
	for _, f := range original {
		if len(f.Stages) == 0 {
			return nil, fmt.Errorf("empty case %s", f.Name)
		}
		single := f
		single.Name = "one-off/" + f.Name
		single.Stages = append([]admission.Stage(nil), f.Stages[:1]...)
		b.c.Corpus.Cases = append(b.c.Corpus.Cases, single)
		hintless := f
		hintless.Name = "no-hint/" + f.Name
		hintless.KeyHint = ""
		b.c.Corpus.Cases = append(b.c.Corpus.Cases, hintless)
	}
	sort.SliceStable(b.c.Corpus.Cases, func(i, j int) bool { return strings.Compare(b.c.Corpus.Cases[i].Name, b.c.Corpus.Cases[j].Name) < 0 })
	if e := b.c.Corpus.Validate(); e != nil {
		return nil, e
	}
	return &b.c.Corpus, nil
}

func verifyPin(path, sha string, size int64) error {
	d := load(path)
	if int64(len(d)) != size || fmt.Sprintf("%x", sha256.Sum256(d)) != sha {
		return fmt.Errorf("capture: pinned input changed: %s", path)
	}
	return nil
}
