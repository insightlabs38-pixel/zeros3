package graphnorm

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"zeros3-z3/simdelta"
)

// Archive stores keyed component histories. A new version is charged as the cheaper of a
// direct object and a B1 similarity delta against one of the latest retained versions of the
// same component (depth-limited, so dependency chains stay bounded and are charged once).
type Archive struct {
	seen     map[[32]byte]bool
	hist     map[string][]*compVer
	MaxDepth int
	Algs     []string
	Total    int64
	Objects  int
	Deltas   int
	DeltaSav int64  // bytes saved relative to direct storage
	Probe    int    // latest versions tried as bases
	Dir      string // when set, every stored object is persisted as a simdelta wire
}

type compVer struct {
	sha   [32]byte
	data  []byte
	depth int
}

func NewArchive(maxDepth int) *Archive {
	return &Archive{seen: map[[32]byte]bool{}, hist: map[string][]*compVer{}, MaxDepth: maxDepth,
		Algs: []string{"adaptive", "git", "rolling"}, Probe: 3}
}

// Put stores one version of component key and returns the incremental cost (object only;
// references are charged by the caller). Every object is a simdelta wire, so the stored
// bytes are exactly what is charged and what the fresh-process replay reads.
func (a *Archive) Put(key string, data []byte) int64 {
	sha := sha256.Sum256(data)
	if a.seen[sha] {
		return 0
	}
	a.seen[sha] = true
	dw, err := simdelta.Encode("direct", nil, data)
	if err != nil {
		panic(err)
	}
	direct := int64(simdelta.Physical(dw))
	best, bestWire, depth := direct, dw, 0
	h := a.hist[key]
	for i := len(h) - 1; i >= 0 && i >= len(h)-a.Probe; i-- {
		base := h[i]
		if base.depth+1 > a.MaxDepth || len(data) < 64 {
			continue
		}
		for _, alg := range a.Algs {
			w, err := simdelta.Encode(alg, [][]byte{base.data}, data)
			if err != nil {
				continue
			}
			if c := int64(simdelta.Physical(w)); c < best {
				best, bestWire, depth = c, w, base.depth+1
			}
		}
	}
	if depth > 0 {
		a.Deltas++
		a.DeltaSav += direct - best
	}
	if a.Dir != "" {
		if err := os.MkdirAll(filepath.Join(a.Dir, "objs"), 0o755); err != nil {
			panic(err)
		}
		if err := os.WriteFile(filepath.Join(a.Dir, "objs", hex.EncodeToString(sha[:])), bestWire, 0o644); err != nil {
			panic(err)
		}
	}
	a.hist[key] = append(h, &compVer{sha: sha, data: data, depth: depth})
	a.Total += best
	a.Objects++
	return best
}

func (a *Archive) Ref(n int) int64 { a.Total += int64(n) * RefCost; return int64(n) * RefCost }
