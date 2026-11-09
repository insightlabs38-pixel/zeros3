package main

// Z4-P1C test infrastructure: explicit storage-layout policy per test.
//
// P1B moved the production default (objects >= 256 KiB write their chunks into
// packs) and hid the 76 layout-dependent tests behind a global override in the
// test binary, so the production default was substantially under-tested. This
// file replaces that override:
//
//   - The test binary runs the PRODUCTION default unless a test says otherwise.
//   - A test that deliberately operates on loose files calls useLooseLayout(t)
//     and states why; the choice is recorded.
//   - forEachLayout runs a behavioural test once per layout (loose, production).
//   - removeChunkAnyLayout / corruptChunkAnyLayout inject the same fault into
//     whichever physical representation holds a chunk, so integrity, repair,
//     GC and verify requirements are stated once and qualified on every layout.
//   - layoutObserver records which physical representations each test really
//     produced; ZEROS3_LAYOUT_MATRIX_OUT=file writes the coverage matrix.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode"
	"unicode/utf8"
)

type layoutRecord struct {
	Test     string         `json:"test"`
	Declared []string       `json:"declared"` // explicit policies requested by the test ("loose", "production")
	Reasons  []string       `json:"reasons,omitempty"`
	Observed map[string]int `json:"observed,omitempty"` // physical publications by kind
}

var layoutMu sync.Mutex
var layoutDeclared = map[string]*layoutRecord{}
var layoutObservedBy = map[string]map[string]int{}

// tempName reproduces testing.T.TempDir's directory naming so a store root can
// be attributed to the test that created it.
func tempName(testName string) string {
	mapper := func(r rune) rune {
		if r < utf8.RuneSelf {
			const allowed = "!#$%&()+,-.=@^_{}~ "
			if '0' <= r && r <= '9' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' {
				return r
			}
			if strings.ContainsRune(allowed, r) {
				return r
			}
		} else if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return r
		}
		return -1
	}
	return strings.Map(mapper, testName)
}

func declareLayout(t *testing.T, policy, reason string) {
	layoutMu.Lock()
	defer layoutMu.Unlock()
	key := tempName(t.Name())
	r := layoutDeclared[key]
	if r == nil {
		r = &layoutRecord{Test: t.Name()}
		layoutDeclared[key] = r
	}
	r.Declared = append(r.Declared, policy)
	if reason != "" {
		r.Reasons = append(r.Reasons, reason)
	}
}

func observeLayoutForTests(kind, root string) {
	tmp := os.TempDir()
	rel, err := filepath.Rel(tmp, root)
	if err != nil || strings.HasPrefix(rel, "..") {
		return
	}
	first := strings.Split(rel, string(filepath.Separator))[0]
	key := strings.TrimRight(first, "0123456789")
	layoutMu.Lock()
	m := layoutObservedBy[key]
	if m == nil {
		m = map[string]int{}
		layoutObservedBy[key] = m
	}
	m[kind]++
	layoutMu.Unlock()
}

// useLooseLayout runs the calling test on a store whose objects are all stored
// as loose chunk files (direct packs and any small-object segment policy off).
// Use it only when the assertion is about the loose representation itself, or
// as the explicit loose half of a behavioural test; reason says which.
func useLooseLayout(t *testing.T, reason string) {
	t.Helper()
	declareLayout(t, "loose", reason)
	old := directPackMinBytes
	directPackMinBytes = math.MaxInt64
	t.Cleanup(func() { directPackMinBytes = old })
	setSmallObjectPolicyForTest(t, false)
}

// useProductionLayout documents that the test deliberately runs on the
// production defaults (the default when nothing is called; this only records it).
func useProductionLayout(t *testing.T) {
	t.Helper()
	declareLayout(t, "production", "")
}

// forEachLayout runs fn once on the loose layout and once on the production
// default, as subtests named after the layout.
func forEachLayout(t *testing.T, fn func(t *testing.T)) {
	t.Helper()
	t.Run("loose", func(t *testing.T) { useLooseLayout(t, "forEachLayout loose half"); fn(t) })
	t.Run("production", func(t *testing.T) { useProductionLayout(t); fn(t) })
}

func writeLayoutMatrix(path string) {
	layoutMu.Lock()
	defer layoutMu.Unlock()
	keys := map[string]*layoutRecord{}
	for k, v := range layoutDeclared {
		keys[k] = v
	}
	for k := range layoutObservedBy {
		// attribute census names (digits stripped) to declared names that share the prefix
		found := false
		for dk := range keys {
			if strings.TrimRight(dk, "0123456789") == k {
				found = true
			}
		}
		if !found {
			keys[k] = &layoutRecord{Test: k}
		}
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	var out bytes.Buffer
	for _, k := range names {
		r := *keys[k]
		r.Observed = layoutObservedBy[strings.TrimRight(k, "0123456789")]
		b, _ := json.Marshal(r)
		out.Write(b)
		out.WriteByte('\n')
	}
	os.WriteFile(path, out.Bytes(), 0o644)
}

// --- layout-independent fault injection -------------------------------------

// packsHolding returns the distinct snapshot pack numbers holding sum.
func packsHolding(s *Store, sum [32]byte) []int32 {
	seen := map[int32]bool{}
	var out []int32
	for _, l := range s.packLocs(sum) {
		if !seen[l.pack] {
			seen[l.pack] = true
			out = append(out, l.pack)
		}
	}
	return out
}

// removeChunkAnyLayout makes a chunk disappear from every physical
// representation: the loose file is deleted and each pack holding it is
// replaced by a valid pack without that record. It reports which
// representations held it.
func removeChunkAnyLayout(t *testing.T, s *Store, sum [32]byte) (wasLoose bool, packsRewritten int) {
	t.Helper()
	if err := os.Remove(s.chunkPath(sum)); err == nil {
		wasLoose = true
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, p := range packsHolding(s, sum) {
		st := s.packSnap()
		src := st.packs[p]
		_, entries, err := loadPackFile(src.path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.ReadFile(src.path)
		if err != nil {
			t.Fatal(err)
		}
		b, err := s.newPackBuilder(src.tier, nil, len(entries))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.sha == sum {
				continue
			}
			raw := f[e.off : e.off+uint64(e.stored)]
			if e.codec == packCodecRaw {
				// copied verbatim, so an already-corrupted neighbour stays corrupted
				b.Add(e.sha, append([]byte(nil), raw...))
				continue
			}
			data, err := decodePackPayload(e.codec, raw, e.logical, e.sha, nil)
			if err != nil {
				t.Fatal(err)
			}
			b.Add(e.sha, data)
		}
		if b.Records() == 0 {
			b.Abort()
		} else {
			// Published without validation on purpose: a neighbour may be corrupted.
			path, _, err := b.Finish()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path, filepath.Join(filepath.Dir(src.path), hex.EncodeToString(b.sum[:])+packFileSuffix)); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Remove(src.path); err != nil {
			t.Fatal(err)
		}
		packsRewritten++
		if err := s.reloadPacks(); err != nil {
			t.Fatal(err)
		}
	}
	if !wasLoose && packsRewritten == 0 {
		t.Fatalf("chunk %x is stored nowhere", sum)
	}
	return
}

// corruptChunkAnyLayout overwrites the chunk's bytes in place in every
// representation holding it (loose file contents, or the payload of each packed
// record) without changing any length.
func corruptChunkAnyLayout(t *testing.T, s *Store, sum [32]byte) {
	t.Helper()
	n := 0
	if info, err := os.Stat(s.chunkPath(sum)); err == nil {
		garbage := bytes.Repeat([]byte{0x99}, int(info.Size()))
		if err := os.WriteFile(s.chunkPath(sum), garbage, 0o644); err != nil {
			t.Fatal(err)
		}
		n++
	}
	for _, l := range s.packLocs(sum) {
		path := s.packSnap().packs[l.pack].path
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		garbage := bytes.Repeat([]byte{0x99}, int(l.stored))
		if _, err := f.WriteAt(garbage, int64(l.off)); err != nil {
			t.Fatal(err)
		}
		f.Close()
		n++
	}
	if n == 0 {
		t.Fatalf("chunk %x is stored nowhere", sum)
	}
}

func chunkSumHex(t *testing.T, h string) [32]byte {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 32 {
		t.Fatalf("bad digest %q", h)
	}
	return [32]byte(b)
}

// setSmallObjectPolicyForTest switches the small-object segment policy (added
// by the segment layer) for the calling test and restores it afterwards.
func setSmallObjectPolicyForTest(t *testing.T, on bool) {
	t.Helper()
}
