package semcas

import (
	"bytes"
	"math/rand"
	"os"
	"sync"
	"testing"
)

var (
	dirMu sync.Mutex
	dirs  = map[*Store]string{}
)

type dirMap struct{}

func (dirMap) get(s *Store) string { dirMu.Lock(); defer dirMu.Unlock(); return dirs[s] }

// dirOf maps a test store to its directory (the core Store is backend-agnostic).
var dirOfM = dirMap{}

func newStore(t *testing.T) *Store {
	t.Helper()
	d := t.TempDir()
	s, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	dirMu.Lock()
	dirs[s] = d
	dirMu.Unlock()
	return s
}

func rnd(n int, seed int64) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewSource(seed))
	r.Read(b)
	return b
}

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func replayAll(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	s2, err := Open(dir) // fresh open: only serialized records, blobs and recipes
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		rc, err := s2.GetRecipe(PayloadSHA(data))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var out bytes.Buffer
		if err := s2.Replay(rc, &out, 1<<20); err != nil {
			t.Fatalf("%s: replay: %v", name, err)
		}
		if !bytes.Equal(out.Bytes(), data) {
			t.Fatalf("%s: replay differs", name)
		}
	}
}
