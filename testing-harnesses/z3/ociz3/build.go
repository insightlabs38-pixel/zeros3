package ociz3

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"zeros3-z3/corpus"
	"zeros3-z3/fstate"
)

// ---- deterministic synthetic layers ----

// Member is one tar entry of a synthetic layer.
type Member struct {
	Path         string
	Mode         int64
	UID, GID     int
	MTime        int64
	Type         byte   // 0 = regular file
	Link         string // symlink / hardlink target
	Data         []byte
	Uname, Gname string
}

var words = []string{"alloc", "buffer", "config", "daemon", "entry", "flush", "group", "handler", "inode", "kernel", "locale", "module", "netlink", "option", "package", "queue", "resolver", "socket", "thread", "update", "vector", "worker"}

func text(r *corpus.RNG, n int) []byte {
	var b bytes.Buffer
	for b.Len() < n {
		b.WriteString(words[r.Intn(len(words))])
		if r.Intn(9) == 0 {
			fmt.Fprintf(&b, "_%d", r.Intn(512))
			b.WriteByte('\n')
		} else {
			b.WriteByte(' ')
		}
	}
	return b.Bytes()[:n]
}

// Tree returns a deterministic file tree: mostly text files with some binary-like
// ones, sizes drawn around mean.
func Tree(seed uint64, label string, files, mean int) []Member {
	r := corpus.NewRNG(seed, "oci-tree/"+label)
	var ms []Member
	for i := 0; i < files; i++ {
		sz := mean/2 + r.Intn(mean+1)
		var data []byte
		if r.Intn(5) == 0 {
			data = r.Bytes(sz)
		} else {
			data = text(r, sz)
		}
		ms = append(ms, Member{Path: fmt.Sprintf("%s/d%02d/f%05d", label, i%17, i), Mode: 0o644, UID: 0, GID: 0, MTime: 1700000000, Data: data})
	}
	return ms
}

// WriteTar serialises members with archive/tar in the given format.
func WriteTar(ms []Member, format tar.Format) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, m := range ms {
		h := &tar.Header{Name: m.Path, Mode: m.Mode, Uid: m.UID, Gid: m.GID, ModTime: timeUnix(m.MTime), Typeflag: m.Type, Linkname: m.Link, Uname: m.Uname, Gname: m.Gname, Format: format}
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(m.Data))
		}
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write(m.Data); err != nil {
				return nil, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- gzip producers ----

// GzipOpts selects a producer for a layer's compression.
type GzipOpts struct {
	Producer string // "go" (default) | "gnugzip" | "python"
	Level    int    // 1..9 (0 = default 6)
	MTime    uint32 // go producer: header mtime
	Name     string // go producer: header name
	OS       byte   // go producer: header OS byte (0 = Go default 255)
}

// Compress gzips b with the selected producer. External producers are optional
// research tools; their versions are reported by ProducerVersions.
func Compress(b []byte, o GzipOpts) ([]byte, error) {
	lvl := o.Level
	if lvl == 0 {
		lvl = 6
	}
	switch o.Producer {
	case "", "go":
		var buf bytes.Buffer
		zw, err := gzip.NewWriterLevel(&buf, lvl)
		if err != nil {
			return nil, err
		}
		zw.ModTime = timeUnix(int64(o.MTime))
		zw.Name = o.Name
		if o.OS != 0 {
			zw.OS = o.OS
		}
		if _, err := zw.Write(b); err != nil {
			return nil, err
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case "gnugzip":
		cmd := exec.Command("gzip", "-n", fmt.Sprintf("-%d", lvl), "-c")
		cmd.Stdin = bytes.NewReader(b)
		return cmd.Output()
	case "python":
		cmd := exec.Command("python3", "-I", "-c", fmt.Sprintf("import gzip,sys;sys.stdout.buffer.write(gzip.compress(sys.stdin.buffer.read(),compresslevel=%d,mtime=0))", lvl))
		cmd.Stdin = bytes.NewReader(b)
		return cmd.Output()
	}
	return nil, fmt.Errorf("unknown gzip producer %q", o.Producer)
}

// ProducerVersions reports the external tool versions used (for provenance).
func ProducerVersions() map[string]string {
	v := map[string]string{}
	if out, err := exec.Command("gzip", "--version").Output(); err == nil {
		v["gnugzip"] = strings.SplitN(string(out), "\n", 2)[0]
	}
	if out, err := exec.Command("python3", "-I", "-c", "import sys,zlib;print('python',sys.version.split()[0],'zlib',zlib.ZLIB_RUNTIME_VERSION)").Output(); err == nil {
		v["python"] = strings.TrimSpace(string(out))
	}
	return v
}

// ---- images ----

// LayerSpec is one layer: the uncompressed tar and its compressed blob (nil =
// store the tar uncompressed).
type LayerSpec struct {
	Tar  []byte
	Blob []byte // gzip of Tar, or nil for an uncompressed layer
}

// ImageSpec describes a synthetic image.
type ImageSpec struct {
	OS, Arch            string
	Variant             string
	Layers              []LayerSpec
	Env                 []string
	ConfigAnnotations   map[string]string // placed in the config's "labels"
	ManifestAnnotations map[string]string
	Created             string
	MediaTypes          string // "oci" (default) | "docker"
}

type cfgJSON struct {
	Created      string `json:"created,omitempty"`
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant,omitempty"`
	Config       struct {
		Env    []string          `json:"Env,omitempty"`
		Labels map[string]string `json:"Labels,omitempty"`
	} `json:"config"`
	RootFS struct {
		Type    string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`
}

type manJSON struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Config        Descriptor        `json:"config"`
	Layers        []Descriptor      `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// Built is a constructed image: exact manifest/config bytes and its blobs.
type Built struct {
	Manifest, Config []byte
	Digest           string // manifest digest
	Desc             Descriptor
	Blobs            MemStore
}

// Build creates the config and manifest documents deterministically (struct field
// order, sorted map keys) and the blob store.
func (s ImageSpec) Build() (*Built, error) {
	b := &Built{Blobs: MemStore{}}
	mtCfg, mtMan, mtGz, mtTar := MTOCIConfig, MTOCIManifest, MTOCILayerGzip, MTOCILayerTar
	if s.MediaTypes == "docker" {
		mtCfg, mtMan, mtGz, mtTar = MTDockerConfig, MTDockerManifest, MTDockerLayerGz, MTDockerLayerTar
	}
	var cfg cfgJSON
	cfg.Created, cfg.Architecture, cfg.OS, cfg.Variant = s.Created, s.Arch, s.OS, s.Variant
	cfg.Config.Env, cfg.Config.Labels = s.Env, s.ConfigAnnotations
	cfg.RootFS.Type = "layers"
	var man manJSON
	man.SchemaVersion, man.MediaType, man.Annotations = 2, mtMan, s.ManifestAnnotations
	for _, l := range s.Layers {
		cfg.RootFS.DiffIDs = append(cfg.RootFS.DiffIDs, Sum256(l.Tar))
		blob, mt := l.Blob, mtGz
		if blob == nil {
			blob, mt = l.Tar, mtTar
		}
		d := b.Blobs.Put(blob)
		man.Layers = append(man.Layers, Descriptor{MediaType: mt, Digest: d, Size: int64(len(blob))})
	}
	cb, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	b.Config = cb
	man.Config = Descriptor{MediaType: mtCfg, Digest: b.Blobs.Put(cb), Size: int64(len(cb))}
	mb, err := json.Marshal(man)
	if err != nil {
		return nil, err
	}
	b.Manifest = mb
	b.Digest = b.Blobs.Put(mb)
	b.Desc = Descriptor{MediaType: mtMan, Digest: b.Digest, Size: int64(len(mb)), Platform: &Platform{Architecture: s.Arch, OS: s.OS, Variant: s.Variant}}
	return b, nil
}

// RootIndex builds an index.json (or an image index blob) listing descriptors.
func RootIndex(descs []Descriptor, annotations map[string]string, mediaType string) ([]byte, error) {
	ix := Index{SchemaVersion: 2, MediaType: mediaType, Manifests: descs, Annotations: annotations}
	return json.Marshal(ix)
}

// LayoutMarker is the exact oci-layout file.
var LayoutMarker = []byte(`{"imageLayoutVersion":"1.0.0"}`)

// Files lists a walked image as lab files: oci-layout, index.json and every
// verified blob under blobs/sha256/<hex> (blobs that failed verification keep their
// path and opaque kind, so a broken image is still a representable file set).
func Files(img *Image) []fstate.File {
	fs := []fstate.File{{Name: "oci-layout", Kind: "layout", Data: LayoutMarker}, {Name: "index.json", Kind: "index-root", Data: img.Root}}
	for _, n := range img.Nodes {
		if n.Data == nil {
			continue
		}
		hexs, err := ParseDigest(n.Digest)
		if err != nil {
			continue
		}
		fs = append(fs, fstate.File{Name: "blobs/sha256/" + hexs, Kind: string(n.Kind), Data: n.Data})
	}
	return fs
}

// Archive serialises a file set as a deterministic tar (the "OCI archive /
// export": sorted names, mtime 0, mode 0644) for the raw-CDC-over-export baseline.
func Archive(files []fstate.File) []byte {
	fs := append([]fstate.File(nil), files...)
	sort.Slice(fs, func(i, j int) bool { return fs[i].Name < fs[j].Name })
	var ms []Member
	for _, f := range fs {
		ms = append(ms, Member{Path: f.Name, Mode: 0o644, Data: f.Data})
	}
	b, _ := WriteTar(ms, tar.FormatPAX)
	return b
}
