package prodshadow

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"zeros3-z3/repz3/kinds"
)

func dirDigest(t *testing.T, dir string) [32]byte {
	t.Helper()
	h := sha256.New()
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dir, p)
			h.Write([]byte(rel))
			h.Write(b)
		}
		return nil
	})
	var s [32]byte
	h.Sum(s[:0])
	return s
}

// The production journal and manifests are the only authoritative namespace state: building,
// publishing, retiring and even failing a shadow never writes either of them.
func TestShadowNeverTouchesJournalOrManifests(t *testing.T) {
	ch := kinds.SQLiteChain(200)
	f, err := NewFixture(bins, filepath.Join(t.TempDir(), "j"), ch, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.L.Stop()
	if _, err := f.Put(0, false, Hooks{}); err != nil {
		t.Fatal(err)
	}
	snap := func() [3][32]byte {
		return [3][32]byte{dirDigest(t, filepath.Join(f.L.Store.Dir, "journal")), dirDigest(t, filepath.Join(f.L.Store.Dir, "manifests")), dirDigest(t, filepath.Join(f.L.Store.Dir, "snapshots"))}
	}
	before := snap()
	if _, err := f.Shadow(0, Hooks{}); err != nil { // authoritative object already committed: shadow after commit
		t.Fatal(err)
	}
	if snap() != before {
		t.Fatal("building a shadow changed production journal/manifests/snapshots")
	}
	if err := f.L.Meta.Retire(f.Roots[0]); err != nil {
		t.Fatal(err)
	}
	if snap() != before {
		t.Fatal("retiring a shadow changed production state")
	}
	// a failed shadow build never rolls back the committed authoritative object
	f2, err := f.Shadow(0, Hooks{AbortAfter: 3})
	_ = f2
	if err == nil {
		t.Fatal("expected injected abort")
	}
	if snap() != before {
		t.Fatal("a failed shadow changed production state")
	}
	if err := f.L.CheckGET(f.Key, ch.Stages[0].Data); err != nil {
		t.Fatal(err)
	}
}
