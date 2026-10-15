// Package ociz3 is the Z3-17 bounded OCI model: image layout, index / manifest /
// config JSON (exact bytes always preserved), descriptor and DiffID verification,
// layer classification, a deterministic synthetic image builder, a minimal
// anonymous registry client and the exact representations the laboratory compares.
//
// It is not a container runtime: no union filesystem, no whiteout application, no
// build engine, no registry server. Whiteouts and opaque markers are ordinary tar
// entries here. Anything it cannot parse or verify is carried as ordinary bytes.
package ociz3

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrCorrupt     = errors.New("ociz3: corrupt")
	ErrLimit       = errors.New("ociz3: limit exceeded")
	ErrDigest      = errors.New("ociz3: digest mismatch")
	ErrUnsupported = errors.New("ociz3: unsupported")
	ErrMissing     = errors.New("ociz3: blob not in store")
)

func corrupt(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(f, a...)) }

// Media types the model understands. Everything else stays an opaque blob.
const (
	MTOCIIndex       = "application/vnd.oci.image.index.v1+json"
	MTOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	MTOCIConfig      = "application/vnd.oci.image.config.v1+json"
	MTOCILayerTar    = "application/vnd.oci.image.layer.v1.tar"
	MTOCILayerGzip   = "application/vnd.oci.image.layer.v1.tar+gzip"
	MTOCILayerZstd   = "application/vnd.oci.image.layer.v1.tar+zstd"
	MTDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
	MTDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	MTDockerConfig   = "application/vnd.docker.container.image.v1+json"
	MTDockerLayerGz  = "application/vnd.docker.image.rootfs.diff.tar.gzip"
	MTDockerLayerTar = "application/vnd.docker.image.rootfs.diff.tar"
)

// Kind classifies a blob for the representations.
type Kind string

const (
	KindIndex    Kind = "index"
	KindManifest Kind = "manifest"
	KindConfig   Kind = "config"
	KindLayerGz  Kind = "layer-gz"
	KindLayerTar Kind = "layer-tar"
	KindBlob     Kind = "blob" // opaque: unknown media type, zstd layer, attestation, mismatching or foreign content
)

// KindOf maps a media type to a Kind. Unknown types are opaque.
func KindOf(mt string) Kind {
	switch mt {
	case MTOCIIndex, MTDockerList:
		return KindIndex
	case MTOCIManifest, MTDockerManifest:
		return KindManifest
	case MTOCIConfig, MTDockerConfig:
		return KindConfig
	case MTOCILayerGzip, MTDockerLayerGz:
		return KindLayerGz
	case MTOCILayerTar, MTDockerLayerTar:
		return KindLayerTar
	}
	return KindBlob
}

// Limits bound every parse.
type Limits struct {
	MaxJSON        int   // bytes of an index / manifest / config document
	MaxDescriptors int   // descriptors per document
	MaxNodes       int   // distinct blobs in one image graph
	MaxDepth       int   // index -> index nesting
	MaxBlob        int64 // bytes of one blob
	MaxInflated    int64 // bytes inflated while verifying a DiffID
}

func DefaultLimits() Limits {
	return Limits{MaxJSON: 4 << 20, MaxDescriptors: 4096, MaxNodes: 100000, MaxDepth: 8, MaxBlob: 4 << 30, MaxInflated: 8 << 30}
}

func (l Limits) norm() Limits {
	d := DefaultLimits()
	if l.MaxJSON <= 0 {
		l.MaxJSON = d.MaxJSON
	}
	if l.MaxDescriptors <= 0 {
		l.MaxDescriptors = d.MaxDescriptors
	}
	if l.MaxNodes <= 0 {
		l.MaxNodes = d.MaxNodes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = d.MaxDepth
	}
	if l.MaxBlob <= 0 {
		l.MaxBlob = d.MaxBlob
	}
	if l.MaxInflated <= 0 {
		l.MaxInflated = d.MaxInflated
	}
	return l
}

// Platform is the descriptor platform subset.
type Platform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant,omitempty"`
}

func (p *Platform) String() string {
	if p == nil {
		return ""
	}
	s := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// Descriptor is the OCI content descriptor subset the model reads.
type Descriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	Platform     *Platform         `json:"platform,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	ArtifactType string            `json:"artifactType,omitempty"`
	URLs         []string          `json:"urls,omitempty"`
}

// ParseDigest validates "<algo>:<hex>" and returns the hex of a sha256 digest.
// Other well-formed algorithms (sha512) are reported as ErrUnsupported: the blob
// stays opaque, its identity is not rederivable here.
func ParseDigest(d string) (string, error) {
	algo, hex, ok := strings.Cut(d, ":")
	if !ok {
		return "", corrupt("digest %q", d)
	}
	switch algo {
	case "sha256":
		if len(hex) != 64 || !isLowerHex(hex) {
			return "", corrupt("sha256 digest %q", d)
		}
		return hex, nil
	case "sha512":
		if len(hex) != 128 || !isLowerHex(hex) {
			return "", corrupt("sha512 digest %q", d)
		}
		return "", ErrUnsupported
	}
	return "", ErrUnsupported
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Manifest is the image manifest subset.
type Manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Config        Descriptor        `json:"config"`
	Layers        []Descriptor      `json:"layers"`
	Subject       *Descriptor       `json:"subject,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// Index is the image index / manifest list subset.
type Index struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType,omitempty"`
	Manifests     []Descriptor      `json:"manifests"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// ImageConfig is the config subset: platform and the DiffIDs of the layers.
type ImageConfig struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	RootFS       struct {
		Type    string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`
}

func checkDesc(d *Descriptor, lim Limits) error {
	if d.Size < 0 || d.Size > lim.MaxBlob {
		return corrupt("descriptor size %d", d.Size)
	}
	if _, err := ParseDigest(d.Digest); err != nil && !errors.Is(err, ErrUnsupported) {
		return err
	}
	return nil
}

func decode(b []byte, v any, lim Limits) error {
	if len(b) > lim.MaxJSON {
		return fmt.Errorf("%w: JSON document of %d bytes", ErrLimit, len(b))
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	if err := dec.Decode(v); err != nil {
		return corrupt("json: %v", err)
	}
	if dec.More() {
		return corrupt("trailing JSON")
	}
	return nil
}

// ParseManifest parses a manifest document. The input bytes are never rewritten:
// callers store the original bytes, the parse only reads descriptors from them.
func ParseManifest(b []byte, lim Limits) (*Manifest, error) {
	lim = lim.norm()
	var m Manifest
	if err := decode(b, &m, lim); err != nil {
		return nil, err
	}
	if len(m.Layers) > lim.MaxDescriptors {
		return nil, fmt.Errorf("%w: %d layers", ErrLimit, len(m.Layers))
	}
	if err := checkDesc(&m.Config, lim); err != nil {
		return nil, err
	}
	for i := range m.Layers {
		if err := checkDesc(&m.Layers[i], lim); err != nil {
			return nil, err
		}
	}
	return &m, nil
}

// ParseIndex parses an image index document.
func ParseIndex(b []byte, lim Limits) (*Index, error) {
	lim = lim.norm()
	var ix Index
	if err := decode(b, &ix, lim); err != nil {
		return nil, err
	}
	if len(ix.Manifests) > lim.MaxDescriptors {
		return nil, fmt.Errorf("%w: %d manifests", ErrLimit, len(ix.Manifests))
	}
	for i := range ix.Manifests {
		if err := checkDesc(&ix.Manifests[i], lim); err != nil {
			return nil, err
		}
	}
	return &ix, nil
}

// ParseConfig parses an image config document.
func ParseConfig(b []byte, lim Limits) (*ImageConfig, error) {
	lim = lim.norm()
	var c ImageConfig
	if err := decode(b, &c, lim); err != nil {
		return nil, err
	}
	if len(c.RootFS.DiffIDs) > lim.MaxDescriptors {
		return nil, fmt.Errorf("%w: %d diff_ids", ErrLimit, len(c.RootFS.DiffIDs))
	}
	for _, d := range c.RootFS.DiffIDs {
		if _, err := ParseDigest(d); err != nil && !errors.Is(err, ErrUnsupported) {
			return nil, err
		}
	}
	return &c, nil
}
