package ociz3

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
)

// Node is one blob of an image graph. Data holds the whole blob: lab-only residency.
// The descriptor/graph logic (Walk, ClassifyRoot, the parsers) reads only documents
// of at most Limits.MaxJSON bytes; a Store-backed integration would keep Data off
// the node and verify blobs by streaming them (size + sha256) instead.
type Node struct {
	Digest    string
	MediaType string
	Size      int64
	Kind      Kind // effective kind: KindBlob when unknown, unsupported, missing or unverified
	Data      []byte
	Status    string // ok | missing | size-mismatch | digest-mismatch | unsupported-digest | unparsable | conflict
	Platform  *Platform
	Parents   []string // distinct digests of the documents that list this blob
	DiffID    string   // layers: the config's diff_id for this layer position ("" if unknown)
	DiffOK    string   // layers: "", "ok", "mismatch", "n/a"

	declKind Kind // KindOf the first descriptor's media type: later descriptors must agree
}

// Image is the verified graph below a root document: an image index / manifest
// list, or a direct image manifest.
type Image struct {
	Root  []byte // layout index.json: the root index itself, or for a direct manifest the one-entry index naming it
	Doc   []byte // exact bytes of the root document as given
	Form  Kind   // KindIndex or KindManifest: what Doc is
	Nodes []*Node
	By    map[string]*Node
	Notes []string
}

// Walk reads the graph below rootDoc (an index, a manifest list, or a direct
// manifest) from st. Every descriptor is checked (size and sha256) before its blob
// is trusted: a blob that fails is kept as an opaque node, the image is still
// representable. A digest is one node with one verified byte string: a later
// descriptor of the same digest must agree on size and kind, otherwise the node
// becomes opaque ("conflict"), never reinterpreted. Documents are parsed only to
// find descriptors; their bytes are never regenerated.
func Walk(st Store, rootDoc []byte, lim Limits) (*Image, error) {
	lim = lim.norm()
	form, rootMT, err := ClassifyRoot(rootDoc, lim)
	if err != nil {
		return nil, err
	}
	img := &Image{Root: rootDoc, Doc: rootDoc, Form: form, By: map[string]*Node{}}
	var visit func(d Descriptor, parent string, depth int) error
	add := func(d Descriptor, parent string) (*Node, bool, error) {
		if n, ok := img.By[d.Digest]; ok {
			if parent != "" && !contains(n.Parents, parent) {
				n.Parents = append(n.Parents, parent)
			}
			if why := conflictOf(n, d); why != "" {
				if n.Status == "ok" {
					n.Status = "conflict"
				}
				n.Kind, n.DiffID, n.DiffOK = KindBlob, "", ""
				img.Notes = append(img.Notes, fmt.Sprintf("descriptor conflict on %s: %s", short(d.Digest), why))
			}
			return n, false, nil
		}
		if len(img.Nodes) >= lim.MaxNodes {
			return nil, false, fmt.Errorf("%w: more than %d blobs", ErrLimit, lim.MaxNodes)
		}
		n := &Node{Digest: d.Digest, MediaType: d.MediaType, Size: d.Size, Kind: KindOf(d.MediaType), Platform: d.Platform}
		n.declKind = n.Kind
		if parent != "" {
			n.Parents = append(n.Parents, parent)
		}
		img.By[d.Digest] = n
		img.Nodes = append(img.Nodes, n)
		hexs, derr := ParseDigest(d.Digest)
		switch {
		case errors.Is(derr, ErrUnsupported):
			n.Status, n.Kind = "unsupported-digest", KindBlob
			return n, false, nil
		case derr != nil:
			n.Status, n.Kind = "unparsable", KindBlob
			return n, false, nil
		}
		if d.Size > lim.MaxBlob {
			n.Status, n.Kind = "size-mismatch", KindBlob
			return n, false, nil
		}
		b, err := st.Blob(d.Digest)
		if err != nil {
			if errors.Is(err, ErrMissing) {
				n.Status, n.Kind = "missing", KindBlob
				return n, false, nil
			}
			return nil, false, err
		}
		n.Data = b
		if int64(len(b)) != d.Size {
			n.Status, n.Kind = "size-mismatch", KindBlob
			return n, false, nil
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != hexs {
			n.Status, n.Kind = "digest-mismatch", KindBlob
			return n, false, nil
		}
		n.Status = "ok"
		return n, true, nil
	}
	visit = func(d Descriptor, parent string, depth int) error {
		n, fresh, err := add(d, parent)
		if err != nil || !fresh {
			return err
		}
		switch n.Kind {
		case KindIndex:
			if depth >= lim.MaxDepth {
				n.Kind, n.Status = KindBlob, "unparsable"
				return nil
			}
			ix, err := ParseIndex(n.Data, lim)
			if err != nil {
				n.Kind, n.Status = KindBlob, "unparsable"
				return nil
			}
			for _, m := range ix.Manifests {
				if err := visit(m, n.Digest, depth+1); err != nil {
					return err
				}
			}
		case KindManifest:
			m, err := ParseManifest(n.Data, lim)
			if err != nil {
				n.Kind, n.Status = KindBlob, "unparsable"
				return nil
			}
			if err := visit(m.Config, n.Digest, depth+1); err != nil {
				return err
			}
			for _, l := range m.Layers {
				if err := visit(l, n.Digest, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	switch form {
	case KindIndex:
		ix, err := ParseIndex(rootDoc, lim)
		if err != nil {
			return nil, err
		}
		for _, d := range ix.Manifests {
			if err := visit(d, "", 0); err != nil {
				return nil, err
			}
		}
	default: // a direct manifest: it becomes an ordinary blob node under a derived one-entry layout index
		if img.Root, err = LayoutRoot(rootDoc); err != nil {
			return nil, err
		}
		d := Descriptor{MediaType: rootMT, Digest: Sum256(rootDoc), Size: int64(len(rootDoc))}
		st = withDoc{st, d.Digest, rootDoc}
		if err := visit(d, "", 0); err != nil {
			return nil, err
		}
	}
	img.assignDiffIDs(lim)
	return img, nil
}

// withDoc serves the root document itself when the store does not hold it.
type withDoc struct {
	Store
	digest string
	doc    []byte
}

func (w withDoc) Blob(d string) ([]byte, error) {
	if d == w.digest {
		return w.doc, nil
	}
	return w.Store.Blob(d)
}

// conflictOf reports how a further descriptor of an already-seen digest disagrees
// with the node: a size other than the verified byte string's (or, for an
// unverified node, the first descriptor's), or a media type of another kind.
func conflictOf(n *Node, d Descriptor) string {
	size := n.Size
	if n.Data != nil {
		size = int64(len(n.Data))
	}
	if d.Size != size {
		return fmt.Sprintf("size %d vs %d", d.Size, size)
	}
	if k := KindOf(d.MediaType); k != n.declKind {
		return fmt.Sprintf("kind %s vs %s", k, n.declKind)
	}
	return ""
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// assignDiffIDs pairs every layer of every verified manifest with the config's
// diff_id at the same position and verifies it against the layer's uncompressed
// content (DiffID = digest of the uncompressed tar; for gzip layers that is not the
// blob digest).
func (img *Image) assignDiffIDs(lim Limits) {
	for _, n := range img.Nodes {
		if n.Kind != KindManifest || n.Status != "ok" {
			continue
		}
		m, err := ParseManifest(n.Data, lim)
		if err != nil {
			continue
		}
		cn := img.By[m.Config.Digest]
		if cn == nil || cn.Status != "ok" || cn.Kind != KindConfig {
			continue
		}
		cfg, err := ParseConfig(cn.Data, lim)
		if err != nil {
			img.Notes = append(img.Notes, fmt.Sprintf("manifest %s: unparsable config", short(n.Digest)))
			continue
		}
		if len(cfg.RootFS.DiffIDs) != len(m.Layers) {
			img.Notes = append(img.Notes, fmt.Sprintf("manifest %s: %d layers, config has %d diff_ids", short(n.Digest), len(m.Layers), len(cfg.RootFS.DiffIDs)))
			continue
		}
		for i, l := range m.Layers {
			ln := img.By[l.Digest]
			if ln == nil || ln.Status != "ok" {
				continue
			}
			ln.DiffID = cfg.RootFS.DiffIDs[i]
			ln.DiffOK = VerifyDiffID(ln, lim)
		}
	}
}

func short(d string) string {
	if len(d) > 19 {
		return d[:19]
	}
	return d
}

// VerifyDiffID checks a layer's DiffID: "ok", "mismatch" or "n/a" (not a layer
// kind we can inflate, or an unsupported digest).
func VerifyDiffID(n *Node, lim Limits) string {
	lim = lim.norm()
	want, err := ParseDigest(n.DiffID)
	if err != nil {
		return "n/a"
	}
	var got [32]byte
	switch n.Kind {
	case KindLayerTar:
		got = sha256.Sum256(n.Data)
	case KindLayerGz:
		zr, err := gzip.NewReader(bytes.NewReader(n.Data))
		if err != nil {
			return "mismatch"
		}
		zr.Multistream(true)
		h := sha256.New()
		defer zr.Close()
		if n, err := io.Copy(h, io.LimitReader(zr, lim.MaxInflated+1)); err != nil || n > lim.MaxInflated {
			return "mismatch"
		}
		copy(got[:], h.Sum(nil))
	default:
		return "n/a"
	}
	if hex.EncodeToString(got[:]) == want {
		return "ok"
	}
	return "mismatch"
}

// Count returns the number of nodes by kind and status.
func (img *Image) Count() map[string]int {
	m := map[string]int{}
	for _, n := range img.Nodes {
		m[string(n.Kind)+"/"+n.Status]++
	}
	return m
}

// SortedDigests returns node digests in first-discovery order sorted for stability.
func (img *Image) SortedDigests() []string {
	var ds []string
	for _, n := range img.Nodes {
		ds = append(ds, n.Digest)
	}
	sort.Strings(ds)
	return ds
}
