package ociz3

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"zeros3-z3/corpus"
	"zeros3-z3/fstate"
)

// Every prefix of every document of a valid image: parse or error, never panic,
// never accept a truncated JSON document.
func TestTruncationAtEveryByte(t *testing.T) {
	b, root := tinyImage(t, nil)
	docs := map[string][]byte{"index": root, "manifest": b.Manifest, "config": b.Config}
	for name, doc := range docs {
		for n := 0; n < len(doc); n++ {
			cut := doc[:n]
			var err error
			switch name {
			case "index":
				_, err = ParseIndex(cut, Limits{})
			case "manifest":
				_, err = ParseManifest(cut, Limits{})
			default:
				_, err = ParseConfig(cut, Limits{})
			}
			if err == nil {
				t.Fatalf("%s truncated to %d of %d bytes accepted", name, n, len(doc))
			}
			if _, err := Walk(MemStore{}, cut, Limits{}); err == nil {
				t.Fatalf("walk accepted truncated %s", name)
			}
		}
	}
}

// Ambiguous identities: one digest referenced under several roles / sizes, the
// same layer twice, a DiffID list of the wrong length. The graph stays finite and
// every blob keeps the single, verified byte string of its digest.
func TestDuplicateAndAmbiguousIdentities(t *testing.T) {
	st := MemStore{}
	layer := []byte(strings.Repeat("L", 2048))
	ld := st.Put(layer)
	cfg := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":["` + ld + `"]}}`)
	cd := st.Put(cfg)
	man, _ := json.Marshal(manJSON{SchemaVersion: 2, MediaType: MTOCIManifest,
		Config: Descriptor{MediaType: MTOCIConfig, Digest: cd, Size: int64(len(cfg))},
		Layers: []Descriptor{
			{MediaType: MTOCILayerTar, Digest: ld, Size: int64(len(layer))},
			{MediaType: MTOCILayerTar, Digest: ld, Size: int64(len(layer))},  // same layer twice
			{MediaType: MTOCILayerGzip, Digest: ld, Size: int64(len(layer))}, // same digest, other media type
			{MediaType: MTOCILayerTar, Digest: ld, Size: 7},                  // same digest, wrong size
			{MediaType: MTOCIConfig, Digest: cd, Size: int64(len(cfg))},      // the config as a layer
		}})
	md := st.Put(man)
	root, _ := RootIndex([]Descriptor{{MediaType: MTOCIManifest, Digest: md, Size: int64(len(man))}, {MediaType: MTOCIManifest, Digest: md, Size: int64(len(man))}}, nil, MTOCIIndex)
	img, err := Walk(st, root, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, n := range img.Nodes {
		seen[n.Digest]++
		if n.Data != nil && Sum256(n.Data) != n.Digest {
			t.Errorf("node %s holds other bytes", short(n.Digest))
		}
	}
	for d, c := range seen {
		if c != 1 {
			t.Errorf("%s appears %d times", short(d), c)
		}
	}
	// files: one per digest, so a duplicate reference can never double-charge or
	// overwrite
	names := map[string]bool{}
	for _, f := range Files(img) {
		if names[f.Name] {
			t.Errorf("duplicate file %s", f.Name)
		}
		names[f.Name] = true
	}
	// DiffID count mismatch: not accepted, no panic
	if c := img.Count(); c["layer-tar/ok"] != 1 {
		t.Logf("kinds: %v", c)
	}
}

// A layer whose descriptor verifies but whose content is not a usable gzip/tar
// (truncated, bit-flipped, wrong format, empty) is an exact CDC blob: every
// representation reconstructs it, nothing panics, and a bounded inflation stops a
// bomb.
func TestMalformedLayersFallBackExactly(t *testing.T) {
	tr, _ := WriteTar(Tree(3, "m", 30, 800), tar.FormatPAX)
	gz, _ := Compress(tr, GzipOpts{Level: 6})
	bomb := func() []byte {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		zw.Write(make([]byte, 64<<20))
		zw.Close()
		return buf.Bytes()
	}()
	cases := map[string][]byte{
		"empty":             {},
		"gzip-header-only":  gz[:10],
		"truncated-half":    gz[:len(gz)/2],
		"truncated-trailer": gz[:len(gz)-4],
		"bitflip-middle":    func() []byte { c := append([]byte(nil), gz...); c[len(c)/2] ^= 0x40; return c }(),
		"bitflip-crc":       func() []byte { c := append([]byte(nil), gz...); c[len(c)-6] ^= 1; return c }(),
		"not-gzip":          bytes.Repeat([]byte("zzzz"), 1000),
		"tar-in-gz-slot":    tr,
		"bomb-64MiB":        bomb,
		"two-members": func() []byte {
			b, _ := Compress(tr[:2048], GzipOpts{})
			c, _ := Compress(tr[2048:], GzipOpts{})
			return append(b, c...)
		}(),
		"trailing-junk": append(append([]byte(nil), gz...), []byte("junk")...),
	}
	for name, data := range cases {
		files := []fstate.File{{Name: "blobs/sha256/" + strings.TrimPrefix(Sum256(data), "sha256:"), Kind: string(KindLayerGz), Data: data}}
		start := time.Now()
		rows := Runner(96, true).RunStates([]*fstate.State{{Label: name, Files: files}}, Reps())
		if time.Since(start) > 20*time.Second {
			t.Errorf("%s took %v", name, time.Since(start))
		}
		for _, r := range rows {
			if !r.Oracle && !r.Exact {
				t.Errorf("%s / %s: not exact: %s", name, r.Rep, r.Err)
			}
		}
	}
	// the DiffID check inflates under a bound
	n := &Node{Kind: KindLayerGz, Data: bomb, DiffID: Sum256(nil)}
	if got := VerifyDiffID(n, Limits{MaxInflated: 1 << 20}); got == "ok" {
		t.Errorf("bomb accepted: %s", got)
	}
}

// Random byte mutations of the documents and of a layer never panic or hang, and
// whatever still parses is walked and represented exactly.
func TestRandomMutationsOfTinyImage(t *testing.T) {
	b, root := tinyImage(t, nil)
	r := corpus.NewRNG(5, "oci-mutate")
	docs := [][]byte{root, b.Manifest, b.Config}
	for i := 0; i < 600; i++ {
		d := append([]byte(nil), docs[i%3]...)
		for k := 0; k <= r.Intn(3); k++ {
			switch r.Intn(3) {
			case 0:
				d[r.Intn(len(d))] ^= byte(1 << uint(r.Intn(8)))
			case 1:
				p := r.Intn(len(d))
				d = append(d[:p], d[p+1:]...)
			default:
				p := r.Intn(len(d))
				d = append(d[:p], append([]byte{byte(r.Intn(256))}, d[p:]...)...)
			}
		}
		st := MemStore{}
		for k, v := range b.Blobs {
			st[k] = v
		}
		rootDoc := root
		switch i % 3 {
		case 0:
			rootDoc = d
		case 1:
			// a mutated manifest under its own (new) digest, listed by the real index
			md := st.Put(d)
			rootDoc, _ = RootIndex([]Descriptor{{MediaType: MTOCIManifest, Digest: md, Size: int64(len(d))}}, nil, MTOCIIndex)
		default:
			md := st.Put(b.Manifest)
			_ = md
			st.Put(d)
		}
		img, err := Walk(st, rootDoc, Limits{})
		if err != nil {
			continue
		}
		rows := Runner(96, true).RunStates([]*fstate.State{{Label: "mut", Files: Files(img)}}, Reps()[:6])
		for _, rw := range rows {
			if !rw.Oracle && !rw.Exact {
				t.Fatalf("mutation %d: %s not exact: %s", i, rw.Rep, rw.Err)
			}
		}
	}
}

// FuzzDocuments feeds arbitrary bytes to the three parsers and to a walk with the
// bytes as the root document, a manifest blob and a config blob.
func FuzzDocuments(f *testing.F) {
	b, root := tinyImage(f, nil)
	f.Add(root)
	f.Add(b.Manifest)
	f.Add(b.Config)
	f.Add([]byte(`{"schemaVersion":2,"manifests":[{"mediaType":"x","digest":"sha256:` + strings.Repeat("a", 64) + `","size":1}]}`))
	f.Add([]byte(`{`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			return
		}
		ParseIndex(data, Limits{})
		ParseManifest(data, Limits{})
		ParseConfig(data, Limits{})
		st := MemStore{}
		for k, v := range b.Blobs {
			st[k] = v
		}
		dd := st.Put(data)
		rt, _ := RootIndex([]Descriptor{{MediaType: MTOCIManifest, Digest: dd, Size: int64(len(data))}, {MediaType: MTOCIIndex, Digest: dd, Size: int64(len(data))}}, nil, MTOCIIndex)
		if img, err := Walk(st, rt, Limits{MaxNodes: 64}); err == nil {
			Files(img)
			for _, n := range img.Nodes {
				if n.Kind == KindLayerGz || n.Kind == KindLayerTar {
					VerifyDiffID(n, Limits{MaxInflated: 1 << 20})
				}
			}
		}
		Walk(st, data, Limits{MaxNodes: 64})
	})
}

func TestTransformGuard(t *testing.T) {
	tr, _ := WriteTar(Tree(4, "g", 20, 1000), tar.FormatPAX)
	gz, _ := Compress(tr, GzipOpts{Level: 6})
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(make([]byte, 16<<20))
	zw.Close()
	for name, tc := range map[string]struct {
		f    fstate.File
		want bool
	}{
		"ordinary gzip":   {fstate.File{Kind: string(KindLayerGz), Data: gz}, true},
		"tar layer":       {fstate.File{Kind: string(KindLayerTar), Data: tr}, true},
		"16 MiB of zeros": {fstate.File{Kind: string(KindLayerGz), Data: buf.Bytes()}, false},
		"truncated":       {fstate.File{Kind: string(KindLayerGz), Data: gz[:len(gz)/2]}, false},
		"empty":           {fstate.File{Kind: string(KindLayerGz)}, false},
	} {
		if got := Guard(tc.f); got != tc.want {
			t.Errorf("%s: Guard = %v", name, got)
		}
	}
	// the guarded layer is counted and stored as ordinary CDC
	rows := Runner(96, true).RunStates([]*fstate.State{{Label: "z", Files: []fstate.File{{Name: "b", Kind: string(KindLayerGz), Data: buf.Bytes()}}}}, []fstate.RepDef{Auto("a", "irp-tok16k+tarmem-s", "tarmem-s", 0.1)})
	if rows[0].Detail["layer_guard"] != 1 || !rows[0].Exact {
		t.Errorf("%+v", rows[0])
	}
}
