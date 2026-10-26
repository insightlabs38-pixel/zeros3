package prodshadow

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// Lab is one production store (Base/store) plus its lab-only shadow metadata
// (Base/shadow). The two never share files: shadow state can be deleted by
// removing Base/shadow, and the store never refers to it.
type Lab struct {
	B     *Binaries
	Base  string
	Store *Store
	Meta  *Meta
	Br    *Bridge
	Sh    *Shadow
	Opt   Options
	Cache int
}

func NewLab(b *Binaries, base string, opt Options) (*Lab, error) {
	l := &Lab{B: b, Base: base, Opt: opt, Cache: 16}
	l.Store = &Store{B: b, Dir: filepath.Join(base, "store")}
	l.Meta = &Meta{Dir: filepath.Join(base, "shadow")}
	for _, d := range []string{l.Store.Dir, l.Meta.Dir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// Start opens the store with the bridge and a fresh Shadow whose descriptors come
// only from the persisted roots (nothing in memory survives a Stop).
func (l *Lab) Start(env ...string) error {
	br, err := l.B.Start(l.Store.Dir, env...)
	if err != nil {
		return err
	}
	l.Br = br
	l.Sh = New(br, l.Meta, l.Opt, l.Cache)
	l.Sh.Adopt()
	return nil
}

func (l *Lab) Stop() error {
	if l.Br == nil {
		return nil
	}
	err := l.Br.Stop()
	l.Br, l.Sh = nil, nil
	return err
}

func (l *Lab) Kill() {
	if l.Br != nil {
		l.Br.Kill()
		l.Br, l.Sh = nil, nil
	}
}

// Hooks inject failures into ShadowRevision.
type Hooks struct {
	AbortAfter    int                // stop staging after this many objects
	BeforePublish func() error       // runs after verification, before the root is written
	MetaHook      func(string) error // see Meta.Publish
	SkipVerify    bool
}

// ShadowRevision builds the shadow of an object that is already committed in the
// production store: stage components, prove the reconstruction through the
// production-backed Source, then publish the root. A failure leaves the
// authoritative object untouched by construction (it is never written here).
func (l *Lab) ShadowRevision(key string, data []byte, b *kinds.Bundle, h Hooks) (*Staged, [32]byte, error) {
	st, err := l.Sh.Stage(b, h.AbortAfter)
	if err != nil {
		return st, [32]byte{}, err
	}
	if !h.SkipVerify {
		var out bytes.Buffer
		n, sum, err := l.Sh.Env().Reconstruct(b.ID, &out)
		if err != nil {
			return st, [32]byte{}, fmt.Errorf("prodshadow: shadow does not reconstruct: %w", err)
		}
		if n != int64(len(data)) || sum != sha256.Sum256(data) || !bytes.Equal(out.Bytes(), data) {
			return st, [32]byte{}, fmt.Errorf("prodshadow: shadow reconstruction differs from the object")
		}
	}
	if h.BeforePublish != nil {
		if err := h.BeforePublish(); err != nil {
			return st, [32]byte{}, err
		}
	}
	id, err := l.Sh.Publish(key, sha256.Sum256(data), int64(len(data)), b.ID, h.MetaHook)
	return st, id, err
}

// LiveFile adopts the published roots, computes the shadow dependency set and
// writes it for Z3_SHADOW_LIVE. It returns the set.
func (l *Lab) LiveFile(path string) (*Live, error) {
	roots, _ := l.Sh.Adopt()
	live, err := l.Sh.LiveSet(roots)
	if err != nil {
		return nil, err
	}
	return live, WriteLive(path, live.Chunks)
}

// Authoritative reads through the ordinary S3 path, compared with want.
func (l *Lab) CheckGET(key string, want []byte) error {
	got, err := l.Br.S3Get(key)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("prodshadow: GET %s differs (%d vs %d bytes)", key, len(got), len(want))
	}
	if len(want) > 8192 {
		off := int64(len(want)) / 3
		r, err := l.Br.S3Range(key, off, 4096)
		if err != nil {
			return err
		}
		if !bytes.Equal(r, want[off:off+4096]) {
			return fmt.Errorf("prodshadow: range GET %s differs", key)
		}
	}
	return nil
}

// CheckShadow reconstructs rep through the production-backed Source and compares.
func (l *Lab) CheckShadow(rep repz3.SHA, want []byte) error {
	var out bytes.Buffer
	n, sum, err := l.Sh.Env().Reconstruct(rep, &out)
	if err != nil {
		return err
	}
	if n != int64(len(want)) || sum != sha256.Sum256(want) || !bytes.Equal(out.Bytes(), want) {
		return fmt.Errorf("prodshadow: shadow reconstruction differs")
	}
	return nil
}

// CopyStore copies the store directory (not the shadow metadata) with cp -a.
func CopyDir(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if out, err := exec.Command("cp", "-a", src, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("cp: %v: %s", err, out)
	}
	return nil
}

// DirBytes sums apparent file sizes under dir; Allocated also reports st_blocks*512.
func DirBytes(dir string) (apparent, allocated int64) {
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			apparent += fi.Size()
			allocated += blocks(fi)
		}
		return nil
	})
	return
}
