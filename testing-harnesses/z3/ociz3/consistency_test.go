package ociz3

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"zeros3-z3/corpus"
	"zeros3-z3/fstate"
)

func blobNames(img *Image) []string {
	var ns []string
	for _, f := range Files(img) {
		if strings.HasPrefix(f.Name, "blobs/") {
			ns = append(ns, f.Name)
		}
	}
	sort.Strings(ns)
	return ns
}

// A walked root is an index, a Docker manifest list, a direct OCI manifest or a
// direct Docker schema-2 manifest; a direct manifest yields the same blob files as
// the same image reached through an index.
func TestRootForms(t *testing.T) {
	for _, mts := range []string{"", "docker"} {
		b, root := tinyImage(t, func(s *ImageSpec) { s.MediaTypes = mts })
		ref, err := Walk(b.Blobs, root, Limits{})
		if err != nil || ref.Form != KindIndex {
			t.Fatalf("%q index: %v", mts, err)
		}
		for name, store := range map[string]Store{"with-manifest-in-store": b.Blobs, "manifest-only-as-root": func() Store {
			m := MemStore{}
			for d, v := range b.Blobs {
				if d != b.Digest {
					m[d] = v
				}
			}
			return m
		}()} {
			img, err := Walk(store, b.Manifest, Limits{})
			if err != nil || img.Form != KindManifest {
				t.Fatalf("%q direct %s: %v", mts, name, err)
			}
			if got, want := strings.Join(blobNames(img), ","), strings.Join(blobNames(ref), ","); got != want {
				t.Errorf("%q direct %s: blobs\n%s\n!=\n%s", mts, name, got, want)
			}
			if string(img.Doc) != string(b.Manifest) || img.By[b.Digest].Kind != KindManifest {
				t.Errorf("%q direct %s: manifest not kept exactly as a blob", mts, name)
			}
			for _, n := range img.Nodes {
				if n.Status != "ok" || ((n.Kind == KindLayerGz || n.Kind == KindLayerTar) && n.DiffOK != "ok") {
					t.Errorf("%q direct %s: %s %s %s diff=%s", mts, name, short(n.Digest), n.Kind, n.Status, n.DiffOK)
				}
			}
			ix, err := ParseIndex(img.Root, Limits{})
			if err != nil || len(ix.Manifests) != 1 || ix.Manifests[0].Digest != b.Digest {
				t.Errorf("derived layout index: %v %+v", err, ix)
			}
			rows := Runner(96, true).RunStates([]*fstate.State{{Label: "direct", Files: Files(img)}}, Reps()[:6])
			for _, r := range rows {
				if !r.Oracle && !r.Exact {
					t.Errorf("direct %s / %s not exact: %s", name, r.Rep, r.Err)
				}
			}
		}
	}
	// a manifest list root, and an OCI manifest that omits its optional mediaType
	b, _ := tinyImage(t, func(s *ImageSpec) { s.MediaTypes = "docker" })
	list, _ := RootIndex([]Descriptor{b.Desc}, nil, MTDockerList)
	if img, err := Walk(b.Blobs, list, Limits{}); err != nil || img.Form != KindIndex || img.Count()["layer-gz/ok"] != 1 {
		t.Fatalf("manifest list: %v", err)
	}
	b, _ = tinyImage(t, nil)
	var m manJSON
	json.Unmarshal(b.Manifest, &m)
	m.MediaType = ""
	bare, _ := json.Marshal(m)
	if img, err := Walk(b.Blobs, bare, Limits{}); err != nil || img.Form != KindManifest || img.Count()["config/ok"] != 1 {
		t.Fatalf("manifest without mediaType: %v", err)
	}
}

func TestRootMalformedAndUnsupported(t *testing.T) {
	for _, c := range []string{``, `{`, `[]`, `"x"`, `{"manifests":[{"digest":"x"}]}`} {
		if _, err := Walk(MemStore{}, []byte(c), Limits{}); !errors.Is(err, ErrCorrupt) {
			t.Errorf("malformed root %q: %v", c, err)
		}
	}
	for _, c := range []string{
		`{}`, `null`, `{"schemaVersion":2}`,
		`{"schemaVersion":1,"mediaType":"application/vnd.docker.distribution.manifest.v1+prettyjws","fsLayers":[]}`,
		`{"mediaType":"application/vnd.oci.artifact.manifest.v1+json"}`,
		`{"mediaType":"application/vnd.oci.image.config.v1+json","config":{},"layers":[]}`,
	} {
		if _, err := Walk(MemStore{}, []byte(c), Limits{}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("unsupported root %q: %v", c, err)
		}
	}
	// a direct manifest with an unusable descriptor walks as one opaque node
	img0, err := Walk(MemStore{}, []byte(`{"config":{"digest":"sha256:zz"},"layers":[]}`), Limits{})
	if err != nil || len(img0.Nodes) != 1 || img0.Nodes[0].Kind != KindBlob || img0.Nodes[0].Status != "unparsable" {
		t.Errorf("bad config digest: %v", err)
	}
	// a direct manifest whose blobs are absent still walks: every node is a recorded gap
	b, _ := tinyImage(t, nil)
	img, err := Walk(MemStore{}, b.Manifest, Limits{})
	if err != nil || img.Count()["blob/missing"] != 3 {
		t.Fatalf("%v %v", err, img.Count())
	}
}

func TestFetchImageDirectManifest(t *testing.T) {
	f := newFakeRegistry(t)
	b, _ := tinyImage(t, nil)
	for d, v := range b.Blobs {
		f.blobs[d] = v
		f.manifest[d] = v
	}
	root, st, notes, err := f.client(t, 0).FetchImage("lib/img", b.Digest, []string{"linux/arm64"})
	if err != nil || len(notes) != 0 || string(root) != string(b.Manifest) {
		t.Fatalf("%v %v", err, notes)
	}
	img, err := Walk(st, root, Limits{})
	if err != nil || img.Form != KindManifest || img.Count()["layer-gz/ok"] != 1 || img.Count()["config/ok"] != 1 {
		t.Fatalf("%v %v", err, img.Count())
	}
}

// dupStore builds a one-layer image by hand so the manifest's descriptors can be
// made to contradict each other.
func dupStore(t *testing.T, layers func(l, c Descriptor) []Descriptor) (*Image, MemStore, string, string) {
	t.Helper()
	st := MemStore{}
	tr, _ := WriteTar(Tree(7, "d", 6, 400), tar.FormatPAX)
	ld := st.Put(tr)
	cfg := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":["` + ld + `"]}}`)
	cd := st.Put(cfg)
	l := Descriptor{MediaType: MTOCILayerTar, Digest: ld, Size: int64(len(tr))}
	c := Descriptor{MediaType: MTOCIConfig, Digest: cd, Size: int64(len(cfg))}
	man, _ := json.Marshal(manJSON{SchemaVersion: 2, MediaType: MTOCIManifest, Config: c, Layers: layers(l, c)})
	md := st.Put(man)
	root, _ := RootIndex([]Descriptor{{MediaType: MTOCIManifest, Digest: md, Size: int64(len(man))}}, nil, MTOCIIndex)
	img, err := Walk(st, root, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range img.Nodes {
		if n.Data != nil && Sum256(n.Data) != n.Digest {
			t.Errorf("node %s holds other bytes", short(n.Digest))
		}
	}
	return img, st, ld, cd
}

func opaque(t *testing.T, img *Image, st MemStore, d string) {
	t.Helper()
	n := img.By[d]
	if n.Kind != KindBlob || n.Status != "conflict" || n.DiffID != "" || n.DiffOK != "" || string(n.Data) != string(st[d]) {
		t.Errorf("%s: kind=%s status=%s diff=%q/%q exact=%v", short(d), n.Kind, n.Status, n.DiffID, n.DiffOK, string(n.Data) == string(st[d]))
	}
	for _, note := range img.Notes {
		if strings.Contains(note, "descriptor conflict on "+short(d)) {
			return
		}
	}
	t.Errorf("%s: no conflict note in %v", short(d), img.Notes)
}

func TestDuplicateDescriptors(t *testing.T) {
	clean := func(name string, img *Image, ld string) {
		t.Helper()
		l := img.By[ld]
		if l.Kind != KindLayerTar || l.Status != "ok" || l.DiffOK != "ok" || len(img.Notes) != 0 || len(l.Parents) != 1 {
			t.Errorf("%s: kind=%s status=%s diff=%s parents=%d notes=%v", name, l.Kind, l.Status, l.DiffOK, len(l.Parents), img.Notes)
		}
	}
	// the identical descriptor, once or repeated, is one node and one parent
	for _, reps := range []int{1, 2, 5} {
		img, _, ld, _ := dupStore(t, func(l, _ Descriptor) []Descriptor {
			var ls []Descriptor
			for i := 0; i < reps; i++ {
				ls = append(ls, l)
			}
			return ls
		})
		if len(img.Nodes) != 3 {
			t.Errorf("%d nodes", len(img.Nodes))
		}
		if reps == 1 {
			clean("single", img, ld)
		} else if l := img.By[ld]; l.Status != "ok" || len(l.Parents) != 1 || len(img.Notes) != 1 {
			// the config lists one diff_id: repeated layers are a count mismatch note, not a conflict
			t.Errorf("x%d: status=%s parents=%v notes=%v", reps, l.Status, l.Parents, img.Notes)
		}
	}
	// wrong size, in either order
	for _, wrongFirst := range []bool{false, true} {
		img, st, ld, _ := dupStore(t, func(l, _ Descriptor) []Descriptor {
			bad := l
			bad.Size = 7
			if wrongFirst {
				return []Descriptor{bad, l}
			}
			return []Descriptor{l, bad}
		})
		n := img.By[ld]
		if wrongFirst {
			if n.Status != "conflict" && n.Status != "size-mismatch" || n.Kind != KindBlob || string(n.Data) != string(st[ld]) {
				t.Errorf("wrong size first: %+v", n.Status)
			}
		} else {
			opaque(t, img, st, ld)
		}
	}
	// one digest as tar and as gzip
	img, st, ld, _ := dupStore(t, func(l, _ Descriptor) []Descriptor {
		g := l
		g.MediaType = MTOCILayerGzip
		return []Descriptor{l, g}
	})
	opaque(t, img, st, ld)
	// config and layer
	img, st, _, cd := dupStore(t, func(l, c Descriptor) []Descriptor {
		as := c
		as.MediaType = MTOCILayerTar
		return []Descriptor{l, as}
	})
	opaque(t, img, st, cd)
	for _, n := range img.Nodes {
		if n.DiffID != "" {
			t.Errorf("%s keeps a diff_id after its config became opaque", short(n.Digest))
		}
	}
	// OCI and Docker spellings of one kind are not a conflict
	img, _, ld, _ = dupStore(t, func(l, _ Descriptor) []Descriptor {
		d := l
		d.MediaType = MTDockerLayerTar
		return []Descriptor{l, d}
	})
	if n := img.By[ld]; n.Status != "ok" || n.Kind != KindLayerTar {
		t.Errorf("OCI+Docker spelling: %s %s", n.Status, n.Kind)
	}
}

// Two manifests that share a config and a layer, and a manifest listed twice with
// different platforms, are legitimate repeated references.
func TestSharedReferencesAreNotConflicts(t *testing.T) {
	st := MemStore{}
	tr, _ := WriteTar(Tree(8, "s", 5, 300), tar.FormatPAX)
	ld := st.Put(tr)
	cfg := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":["` + ld + `"]}}`)
	cd := st.Put(cfg)
	l := Descriptor{MediaType: MTOCILayerTar, Digest: ld, Size: int64(len(tr))}
	c := Descriptor{MediaType: MTOCIConfig, Digest: cd, Size: int64(len(cfg))}
	var mds []Descriptor
	for _, ann := range []string{"a", "b"} {
		m, _ := json.Marshal(manJSON{SchemaVersion: 2, MediaType: MTOCIManifest, Config: c, Layers: []Descriptor{l}, Annotations: map[string]string{"k": ann}})
		mds = append(mds, Descriptor{MediaType: MTOCIManifest, Digest: st.Put(m), Size: int64(len(m))})
	}
	again := mds[0]
	again.Platform = &Platform{OS: "linux", Architecture: "arm64"}
	root, _ := RootIndex(append(mds, mds[0], again), nil, MTOCIIndex)
	img, err := Walk(st, root, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(img.Nodes) != 4 || len(img.Notes) != 0 {
		t.Fatalf("%d nodes, notes %v", len(img.Nodes), img.Notes)
	}
	if n := img.By[ld]; n.Status != "ok" || n.DiffOK != "ok" || len(n.Parents) != 2 {
		t.Errorf("shared layer: %s diff=%s parents=%d", n.Status, n.DiffOK, len(n.Parents))
	}
	if n := img.By[cd]; n.Kind != KindConfig || len(n.Parents) != 2 {
		t.Errorf("shared config: %s parents=%d", n.Kind, len(n.Parents))
	}
}

func mutateDoc(r *corpus.RNG, src []byte) []byte {
	d := append([]byte(nil), src...)
	for k := 0; k <= r.Intn(3); k++ {
		p := r.Intn(len(d))
		switch r.Intn(3) {
		case 0:
			d[p] ^= byte(1 << uint(r.Intn(8)))
		case 1:
			d = append(d[:p], d[p+1:]...)
		default:
			d = append(d[:p], append([]byte{byte(r.Intn(256))}, d[p:]...)...)
		}
	}
	return d
}

// Each mutated object is placed under its own new digest, the parent that must
// reach it is rewritten with a repaired descriptor, and the walk must reach it:
// outer integrity passes, so what is exercised is the parser and the fallbacks.
func TestMutatedObjectsAreTraversed(t *testing.T) {
	b, _ := tinyImage(t, nil)
	var man manJSON
	if err := json.Unmarshal(b.Manifest, &man); err != nil {
		t.Fatal(err)
	}
	r := corpus.NewRNG(5, "oci-mutate")
	desc := func(mt string, d []byte, st MemStore) Descriptor {
		return Descriptor{MediaType: mt, Digest: st.Put(d), Size: int64(len(d))}
	}
	rootFor := func(m manJSON, st MemStore) (root []byte, manifestDigest string) {
		mb, _ := json.Marshal(m)
		md := desc(MTOCIManifest, mb, st)
		md.Platform = b.Desc.Platform
		root, _ = RootIndex([]Descriptor{md}, nil, MTOCIIndex)
		return root, md.Digest
	}
	layerBlob := b.Blobs[man.Layers[0].Digest]
	for _, target := range []string{"root", "manifest", "config", "layer"} {
		for i := 0; i < 30; i++ {
			st := MemStore{}
			for k, v := range b.Blobs {
				st[k] = v
			}
			var root []byte
			var mutated string // digest of the mutated object, which the walk must reach
			m := man
			m.Layers = append([]Descriptor(nil), man.Layers...)
			switch target {
			case "root":
				orig, _ := RootIndex([]Descriptor{b.Desc}, nil, MTOCIIndex)
				root = mutateDoc(r, orig)
			case "manifest":
				mb := mutateDoc(r, b.Manifest)
				d := desc(MTOCIManifest, mb, st)
				root, _ = RootIndex([]Descriptor{d}, nil, MTOCIIndex)
				mutated = d.Digest
			case "config":
				d := desc(MTOCIConfig, mutateDoc(r, b.Config), st)
				m.Config = d
				root, _ = rootFor(m, st)
				mutated = d.Digest
			default:
				d := desc(m.Layers[0].MediaType, mutateDoc(r, layerBlob), st)
				m.Layers[0] = d
				root, _ = rootFor(m, st)
				mutated = d.Digest
			}
			img, err := Walk(st, root, Limits{})
			if err != nil {
				if target != "root" {
					t.Fatalf("%s %d: a repaired reference must walk: %v", target, i, err)
				}
				continue
			}
			if mutated != "" {
				n := img.By[mutated]
				// a mutated manifest may stop parsing: it is then an opaque, still verified node
				if n == nil || n.Data == nil || Sum256(n.Data) != mutated || n.Status != "ok" && !(target == "manifest" && n.Status == "unparsable") {
					t.Fatalf("%s %d: mutated object not reached and verified: %+v", target, i, n)
				}
				if target == "layer" && n.DiffOK == "" {
					t.Fatalf("%s %d: mutated layer reached but its DiffID never checked", target, i)
				}
			}
			rows := Runner(96, true).RunStates([]*fstate.State{{Label: "mut", Files: Files(img)}}, Reps()[:6])
			for _, rw := range rows {
				if !rw.Oracle && !rw.Exact {
					t.Fatalf("%s %d: %s not exact: %s", target, i, rw.Rep, rw.Err)
				}
			}
		}
	}
}

// With the descriptor left broken, the walk refuses to trust the mutated bytes but
// still carries them exactly.
func TestBrokenIntegrityIsRejected(t *testing.T) {
	b, root := tinyImage(t, nil)
	var man manJSON
	json.Unmarshal(b.Manifest, &man)
	for name, d := range map[string]string{"manifest": b.Digest, "config": man.Config.Digest, "layer": man.Layers[0].Digest} {
		for _, grow := range []bool{false, true} {
			st := MemStore{}
			for k, v := range b.Blobs {
				st[k] = v
			}
			mut := append([]byte(nil), st[d]...)
			if grow {
				mut = append(mut, 'x')
			} else {
				mut[len(mut)/2] ^= 1
			}
			st[d] = mut
			img, err := Walk(st, root, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			n := img.By[d]
			if n.Status != "digest-mismatch" && n.Status != "size-mismatch" || n.Kind != KindBlob || string(n.Data) != string(mut) {
				t.Errorf("%s grow=%v: %s %s", name, grow, n.Status, n.Kind)
			}
			if name == "config" || name == "layer" {
				if n.DiffID != "" || n.DiffOK != "" {
					t.Errorf("%s: diff_id assigned through a rejected object", name)
				}
			}
			rows := Runner(96, true).RunStates([]*fstate.State{{Label: "bad", Files: Files(img)}}, Reps()[:6])
			for _, rw := range rows {
				if !rw.Oracle && !rw.Exact {
					t.Errorf("%s: %s not exact", name, rw.Rep)
				}
			}
		}
	}
}

// Every non-oracle representation of the standard OCI set rebuilds through its
// serialised recipe; a representation with a nil reconstruction path would be a
// failed row here, not a silently exact one.
func TestStandardRepSetIsReconstructible(t *testing.T) {
	b, root := tinyImage(t, nil)
	img, err := Walk(b.Blobs, root, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	reps := Reps()
	rows := Runner(96, true).RunStates([]*fstate.State{{Label: "std", Files: Files(img)}}, reps)
	if len(rows) != len(reps) {
		t.Fatalf("%d rows for %d representations", len(rows), len(reps))
	}
	checked := 0
	for _, r := range rows {
		if r.Oracle {
			continue
		}
		checked++
		if !r.Exact || r.Err != "" {
			t.Errorf("%s: exact=%v err=%q", r.Rep, r.Exact, r.Err)
		}
	}
	if checked < 6 {
		t.Errorf("only %d non-oracle representations checked", checked)
	}
}
