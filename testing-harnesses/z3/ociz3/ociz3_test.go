package ociz3

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zeros3-z3/fstate"
)

func tinyImage(t testing.TB, mut func(*ImageSpec)) (*Built, []byte) {
	t.Helper()
	l0, err := WriteTar(Tree(1, "a", 20, 600), tar.FormatPAX)
	if err != nil {
		t.Fatal(err)
	}
	l1, _ := WriteTar(Tree(2, "b", 5, 300), tar.FormatPAX)
	g0, err := Compress(l0, GzipOpts{Level: 6})
	if err != nil {
		t.Fatal(err)
	}
	s := ImageSpec{OS: "linux", Arch: "amd64", Layers: []LayerSpec{{Tar: l0, Blob: g0}, {Tar: l1}}, Env: []string{"A=1"}}
	if mut != nil {
		mut(&s)
	}
	b, err := s.Build()
	if err != nil {
		t.Fatal(err)
	}
	root, err := RootIndex([]Descriptor{b.Desc}, nil, MTOCIIndex)
	if err != nil {
		t.Fatal(err)
	}
	return b, root
}

func TestWalkRoundTrip(t *testing.T) {
	b, root := tinyImage(t, nil)
	img, err := Walk(b.Blobs, root, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	c := img.Count()
	if c["manifest/ok"] != 1 || c["config/ok"] != 1 || c["layer-gz/ok"] != 1 || c["layer-tar/ok"] != 1 {
		t.Fatalf("%v", c)
	}
	for _, n := range img.Nodes {
		if (n.Kind == KindLayerGz || n.Kind == KindLayerTar) && n.DiffOK != "ok" {
			t.Errorf("layer %s: diffid %q", short(n.Digest), n.DiffOK)
		}
	}
	// docker media types also resolve
	b2, root2 := tinyImage(t, func(s *ImageSpec) { s.MediaTypes = "docker" })
	if img2, err := Walk(b2.Blobs, root2, Limits{}); err != nil || img2.Count()["layer-gz/ok"] != 1 {
		t.Fatalf("docker: %v %v", err, img2.Count())
	}
	// the files keep exact bytes
	fs := Files(img)
	if len(fs) != 2+len(img.Nodes) {
		t.Fatalf("%d files for %d nodes", len(fs), len(img.Nodes))
	}
	for _, f := range fs[2:] {
		if !strings.HasPrefix(f.Name, "blobs/sha256/") || "sha256:"+strings.TrimPrefix(f.Name, "blobs/sha256/") != Sum256(f.Data) {
			t.Errorf("file %s does not hold its digest", f.Name)
		}
	}
}

func TestDescriptorAndDiffIDMismatch(t *testing.T) {
	b, root := tinyImage(t, nil)
	// corrupt the gzip layer in the store: digest mismatch -> opaque node, image still walks
	for d, blob := range b.Blobs {
		if len(blob) > 100 && blob[0] == 0x1f && blob[1] == 0x8b {
			c := append([]byte(nil), blob...)
			c[len(c)/2] ^= 0xff
			b.Blobs[d] = c
		}
	}
	img, err := Walk(b.Blobs, root, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, nd := range img.Nodes {
		if nd.Status == "digest-mismatch" {
			n++
			if nd.Kind != KindBlob || nd.Data == nil {
				t.Errorf("mismatching blob should stay an opaque, present blob: %+v", nd.Kind)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d mismatches", n)
	}
	// wrong diff_id in the config (re-signed manifest): DiffID mismatch reported, not accepted
	b2, root2 := tinyImage(t, nil)
	var cfg map[string]any
	json.Unmarshal(b2.Config, &cfg)
	rf := cfg["rootfs"].(map[string]any)
	rf["diff_ids"].([]any)[0] = "sha256:" + strings.Repeat("0", 64)
	cb, _ := json.Marshal(cfg)
	st := MemStore{}
	for d, v := range b2.Blobs {
		st[d] = v
	}
	cd := st.Put(cb)
	var man manJSON
	json.Unmarshal(b2.Manifest, &man)
	man.Config = Descriptor{MediaType: MTOCIConfig, Digest: cd, Size: int64(len(cb))}
	mb, _ := json.Marshal(man)
	md := st.Put(mb)
	root2, _ = RootIndex([]Descriptor{{MediaType: MTOCIManifest, Digest: md, Size: int64(len(mb))}}, nil, MTOCIIndex)
	img2, err := Walk(st, root2, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, nd := range img2.Nodes {
		if nd.DiffOK == "mismatch" {
			bad++
		}
	}
	if bad != 1 {
		t.Fatalf("diffid mismatches: %d", bad)
	}
	_ = root
}

func TestUnknownAndUnsupportedStayOpaque(t *testing.T) {
	st := MemStore{}
	att := []byte(`{"_type":"https://in-toto.io/Statement/v0.1"}`)
	zst := []byte("not really zstd")
	ad, zd := st.Put(att), st.Put(zst)
	cfg := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":["sha256:` + strings.Repeat("a", 64) + `"]}}`)
	cd := st.Put(cfg)
	man, _ := json.Marshal(manJSON{SchemaVersion: 2, MediaType: MTOCIManifest, Config: Descriptor{MediaType: MTOCIConfig, Digest: cd, Size: int64(len(cfg))},
		Layers: []Descriptor{{MediaType: MTOCILayerZstd, Digest: zd, Size: int64(len(zst))}, {MediaType: "application/vnd.in-toto+json", Digest: ad, Size: int64(len(att))},
			{MediaType: MTOCILayerGzip, Digest: "sha512:" + strings.Repeat("b", 128), Size: 5}}})
	md := st.Put(man)
	root, _ := RootIndex([]Descriptor{{MediaType: MTOCIManifest, Digest: md, Size: int64(len(man))}}, nil, MTOCIIndex)
	img, err := Walk(st, root, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	c := img.Count()
	if c["blob/ok"] != 2 || c["blob/unsupported-digest"] != 1 || c["manifest/ok"] != 1 {
		t.Fatalf("%v", c)
	}
	if len(Files(img)) != 2+4 { // 4 nodes with data: manifest, config, zstd, attestation (the sha512 one has none)
		t.Fatalf("files: %d", len(Files(img)))
	}
}

func TestHostileDocuments(t *testing.T) {
	bigArr := `{"schemaVersion":2,"manifests":[` + strings.Repeat(`{"mediaType":"x","digest":"sha256:`+strings.Repeat("a", 64)+`","size":1},`, 5000) + `{"mediaType":"x","digest":"sha256:` + strings.Repeat("a", 64) + `","size":1}]}`
	cases := map[string]string{
		"too-many-descriptors": bigArr,
		"negative-size":        `{"schemaVersion":2,"manifests":[{"mediaType":"x","digest":"sha256:` + strings.Repeat("a", 64) + `","size":-1}]}`,
		"huge-size":            `{"schemaVersion":2,"manifests":[{"mediaType":"x","digest":"sha256:` + strings.Repeat("a", 64) + `","size":99999999999999999}]}`,
		"short-digest":         `{"schemaVersion":2,"manifests":[{"mediaType":"x","digest":"sha256:abc","size":1}]}`,
		"upper-hex":            `{"schemaVersion":2,"manifests":[{"mediaType":"x","digest":"sha256:` + strings.Repeat("A", 64) + `","size":1}]}`,
		"traversal":            `{"schemaVersion":2,"manifests":[{"mediaType":"x","digest":"sha256:../../etc/passwd","size":1}]}`,
		"not-json":             `<xml/>`,
		"trailing":             `{"schemaVersion":2,"manifests":[]} {}`,
		"deep-nesting":         strings.Repeat(`{"a":`, 20000) + `1` + strings.Repeat(`}`, 20000),
	}
	for n, c := range cases {
		if _, err := ParseIndex([]byte(c), Limits{}); err == nil {
			t.Errorf("%s: accepted", n)
		}
	}
	if _, err := ParseIndex(bytes.Repeat([]byte(" "), 5<<20), Limits{}); !errors.Is(err, ErrLimit) {
		t.Errorf("oversized document: %v", err)
	}
	// unknown fields are ignored (exact bytes are kept by the caller)
	if _, err := ParseManifest([]byte(`{"schemaVersion":2,"config":{"mediaType":"c","digest":"sha256:`+strings.Repeat("a", 64)+`","size":1},"layers":[],"future":{"x":[1,2,3]}}`), Limits{}); err != nil {
		t.Errorf("unknown field: %v", err)
	}
}

func TestIndexNestingAndCycleBounded(t *testing.T) {
	st := MemStore{}
	// an index that lists itself cannot be written (digest depends on content); a chain deeper than MaxDepth must stop
	var prev Descriptor
	for i := 0; i < 20; i++ {
		var ds []Descriptor
		if i > 0 {
			ds = []Descriptor{prev}
		}
		ixb, _ := RootIndex(ds, map[string]string{"n": fmt.Sprint(i)}, MTOCIIndex)
		prev = Descriptor{MediaType: MTOCIIndex, Digest: st.Put(ixb), Size: int64(len(ixb))}
	}
	root, _ := RootIndex([]Descriptor{prev}, nil, MTOCIIndex)
	img, err := Walk(st, root, Limits{MaxDepth: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(img.Nodes) > 7 {
		t.Fatalf("walked %d nodes past the depth bound", len(img.Nodes))
	}
	if _, err := Walk(st, root, Limits{MaxNodes: 3}); !errors.Is(err, ErrLimit) {
		t.Fatalf("node bound: %v", err)
	}
}

func TestDirStoreCannotEscape(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "blobs", "sha256"), 0o755)
	secret := filepath.Join(filepath.Dir(dir), "secret")
	os.WriteFile(secret, []byte("x"), 0o644)
	ds := DirStore{Root: dir}
	for _, d := range []string{"sha256:../../../secret", "sha256:" + strings.Repeat("/", 64), "../secret", "sha256:" + strings.Repeat("a", 63) + "/"} {
		if _, err := ds.Blob(d); err == nil || errors.Is(err, os.ErrNotExist) && false {
			t.Errorf("%q accepted", d)
		}
	}
	b := []byte("hello")
	hexs := strings.TrimPrefix(Sum256(b), "sha256:")
	os.WriteFile(filepath.Join(dir, "blobs", "sha256", hexs), b, 0o644)
	if got, err := ds.Blob(Sum256(b)); err != nil || string(got) != "hello" {
		t.Fatalf("%v", err)
	}
	if _, err := ds.Blob("sha256:" + strings.Repeat("0", 64)); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := (DirStore{Root: dir, MaxBlob: 2}).Blob(Sum256(b)); !errors.Is(err, ErrLimit) {
		t.Fatalf("blob bound: %v", err)
	}
}

func TestBuilderIsDeterministic(t *testing.T) {
	a, _ := tinyImage(t, nil)
	b, _ := tinyImage(t, nil)
	if a.Digest != b.Digest {
		t.Fatal("two builds differ")
	}
	_ = time.Now
}

func stateOf(t testing.TB, label string, b *Built, root []byte) *fstate.State {
	t.Helper()
	img, err := Walk(b.Blobs, root, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return &fstate.State{Label: label, Files: Files(img)}
}

// Every representation reconstructs every blob and archive exactly, through the
// serialised recipe, for a base image and a revision with one changed layer.
func TestRepsExactOnImageRevision(t *testing.T) {
	mk := func(mut func([]Member) []Member) *fstate.State {
		ms := Tree(7, "base", 120, 3000)
		if mut != nil {
			ms = mut(ms)
		}
		tr, _ := WriteTar(ms, tar.FormatPAX)
		gz, err := Compress(tr, GzipOpts{Level: 6})
		if err != nil {
			t.Fatal(err)
		}
		app, _ := WriteTar(Tree(8, "app", 10, 500), tar.FormatPAX)
		b, root := tinyImageLayers(t, []LayerSpec{{Tar: tr, Blob: gz}, {Tar: app}})
		return stateOf(t, "x", b, root)
	}
	a := mk(nil)
	a.Label = "A"
	bs := mk(func(ms []Member) []Member {
		ms[40].Data = append(append([]byte(nil), ms[40].Data...), []byte(" changed")...)
		return ms
	})
	bs.Label = "B (one file changed)"
	r := Runner(96, true)
	rows := r.RunStates([]*fstate.State{a, bs}, Reps())
	byRep := map[string]fstate.Row{}
	for _, row := range rows {
		if !row.Oracle && !row.Exact {
			t.Errorf("%s/%s not exact: %s", row.State, row.Rep, row.Err)
		}
		if row.State == bs.Label {
			byRep[row.Rep] = row
		}
	}
	if byRep["layer-tar+gzip"].CostPacked*3 > byRep["cdc"].CostPacked {
		t.Errorf("tar+gzip %d vs cdc %d for a one-file change", byRep["layer-tar+gzip"].CostPacked, byRep["cdc"].CostPacked)
	}
	if byRep["oci-auto"].CostPacked > byRep["cdc"].CostPacked {
		t.Errorf("auto %d worse than cdc %d", byRep["oci-auto"].CostPacked, byRep["cdc"].CostPacked)
	}
}

func tinyImageLayers(t testing.TB, ls []LayerSpec) (*Built, []byte) {
	t.Helper()
	b, err := ImageSpec{OS: "linux", Arch: "amd64", Layers: ls}.Build()
	if err != nil {
		t.Fatal(err)
	}
	root, _ := RootIndex([]Descriptor{b.Desc}, nil, MTOCIIndex)
	return b, root
}
