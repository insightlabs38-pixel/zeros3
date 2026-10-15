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

// Node is one blob of an image graph.
type Node struct {
	Digest    string
	MediaType string
	Size      int64
	Kind      Kind // effective kind: KindBlob when unknown, unsupported, missing or unverified
	Data      []byte
	Status    string // ok | missing | size-mismatch | digest-mismatch | unsupported-digest | unparsable
	Platform  *Platform
	Parents   []string // digests of the documents that list this blob
	DiffID    string   // layers: the config's diff_id for this layer position ("" if unknown)
	DiffOK    string   // layers: "", "ok", "mismatch", "n/a"
}

// Image is the verified graph below a root document (index.json or a manifest).
type Image struct {
	Root  []byte // exact bytes of the root document
	Nodes []*Node
	By    map[string]*Node
	Notes []string
}

// Walk reads the graph below rootDoc (an index or a manifest) from st. Every
// descriptor is checked (size and sha256) before its blob is trusted: a blob that
// fails is kept as an opaque node, the image is still representable. Documents are
// parsed only to find descriptors; their bytes are never regenerated.
func Walk(st Store, rootDoc []byte, lim Limits) (*Image, error) {
	lim = lim.norm()
	img := &Image{Root: rootDoc, By: map[string]*Node{}}
	var visit func(d Descriptor, parent string, depth int) error
	add := func(d Descriptor, parent string) (*Node, bool, error) {
		if n, ok := img.By[d.Digest]; ok {
			if parent != "" {
				n.Parents = append(n.Parents, parent)
			}
			return n, false, nil
		}
		if len(img.Nodes) >= lim.MaxNodes {
			return nil, false, fmt.Errorf("%w: more than %d blobs", ErrLimit, lim.MaxNodes)
		}
		n := &Node{Digest: d.Digest, MediaType: d.MediaType, Size: d.Size, Kind: KindOf(d.MediaType), Platform: d.Platform}
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
	// the root document: an index, a manifest, or (index.json of a layout) an index whose
	// entries point at either
	ix, ierr := ParseIndex(rootDoc, lim)
	if ierr != nil {
		return nil, ierr
	}
	for _, d := range ix.Manifests {
		if err := visit(d, "", 0); err != nil {
			return nil, err
		}
	}
	img.assignDiffIDs(lim)
	return img, nil
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
		if err != nil || len(cfg.RootFS.DiffIDs) != len(m.Layers) {
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
		if _, err := io.Copy(h, io.LimitReader(zr, lim.MaxInflated+1)); err != nil {
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
