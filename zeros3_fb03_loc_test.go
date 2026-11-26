package main

// FB-03 M-A8: locator levels, filters, lean open (section 4c).

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// loadPackStateRef is the open-time loader as it was before FB-03 (per-pack
// entry lists and digest maps): the reference the lean open must match.
func loadPackStateRef(root string, format int) (*packState, error) {
	type found struct {
		t    tier
		dir  string
		name string
	}
	var files []found
	hint := 0
	for t := tierHot; t < numTiers; t++ {
		if t != tierHot && format < storeFormatVersionTiers {
			break
		}
		dir := tierPackDir(root, t)
		ents, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) && t == tierHot {
				continue
			}
			return nil, err
		}
		for _, e := range ents {
			if !e.IsDir() && isPackFileName(e.Name()) {
				files = append(files, found{t, dir, e.Name()})
				hint += packRecordHint(filepath.Join(dir, e.Name()))
			}
		}
	}
	if hint >= math.MaxUint32 {
		return nil, errors.New("pack locator is full: too many packed records")
	}
	st := &packState{}
	recs := make([]locRec, 0, hint)
	// Each pack's footer and index are read concurrently; they are folded in below
	// in directory order, so the locator is the one a serial load would build.
	type loadedPack struct {
		info    packInfo
		entries []packEntry
		err     error
	}
	loaded := make([]loadedPack, len(files))
	parallelFor(len(files), maintWorkers(), 4, func(i int) {
		l := &loaded[i]
		l.info, l.entries, l.err = loadPackFile(filepath.Join(files[i].dir, files[i].name))
	})
	for fi, f := range files {
		info, entries, err := loaded[fi].info, loaded[fi].entries, loaded[fi].err
		if err != nil {
			name := f.name
			if f.t != tierHot {
				name = f.t.String() + "/" + name
			}
			st.bad = append(st.bad, packProblem{name: name, err: err})
			continue
		}
		if len(st.packs) >= packIdxMask {
			return nil, errors.New("pack locator is full: too many packs")
		}
		info.tier = f.t
		idx := uint32(len(st.packs))
		st.packs = append(st.packs, info)
		for i := range entries {
			if entries[i].codec == packCodecPresetDict {
				continue
			}
			recs = append(recs, mkLocRec(&entries[i], idx, f.t))
		}
		if len(recs) >= math.MaxUint32 {
			return nil, errors.New("pack locator is full: too many packed records")
		}
	}
	st.index(recs)
	return st, nil
}

// ---- open equivalence ------------------------------------------------------

func fb03PackDirs(t *testing.T, root string) {
	t.Helper()
	for tt := tierHot; tt < numTiers; tt++ {
		if err := os.MkdirAll(tierPackDir(root, tt), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// fb03Pack writes a structurally valid pack of recs into tier tt of root.
func fb03Pack(t *testing.T, root string, tt tier, recs []testPackRec) string {
	t.Helper()
	return writeTestPack(t, tierRoot(root, tt), recs)
}

func fb03Rec(sum [32]byte, logical uint32, deflate bool) testPackRec {
	if deflate && logical >= 2 {
		return testPackRec{sum, logical, packCodecDeflate, make([]byte, logical/2)}
	}
	return testPackRec{sum, logical, packCodecRaw, make([]byte, logical)}
}

// fb03Preset lays out a pack-local preset pack: its dictionary (codec 5)
// first, members (codec 4), the dictionary's recovery copy last.
func fb03Preset(rng *rand.Rand, dict [32]byte, members []testPackRec) []testPackRec {
	n := uint32(64 + rng.Intn(512))
	var cp [32]byte
	rng.Read(cp[:])
	out := []testPackRec{{dict, n, packCodecPresetDict, make([]byte, n)}}
	for _, m := range members {
		if m.logical >= 2 {
			out = append(out, testPackRec{m.sum, m.logical, packCodecPreset, make([]byte, m.logical/2)})
		}
	}
	m := n + uint32(len(packDictCopyMagic))
	return append(out, testPackRec{cp, m, packCodecPresetDict, make([]byte, m)})
}

func fb03Rewrite(t *testing.T, path string, fn func([]byte) []byte) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fn(b), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fb03Population writes a pack set that holds everything the open must
// handle: copies of a digest across packs and tiers, contradictory lengths,
// deflate records, preset packs (whose codec-5 dictionary records stay out
// of the locator, sometimes under a digest that is also an ordinary chunk),
// a crowd sharing a 4-byte prefix, and one broken pack per open check.
func fb03Population(t *testing.T, root string, seed int64) {
	t.Helper()
	fb03PackDirs(t, root)
	rng := rand.New(rand.NewSource(seed))
	universe := locTestUniverse(rng)
	logical := map[[32]byte]uint32{}
	for _, s := range universe {
		logical[s] = uint32(1 + rng.Intn(64))
	}
	clash := map[[32]byte]bool{}
	for i := 0; i < 30; i++ {
		clash[universe[rng.Intn(len(universe))]] = true
	}
	pick := func(n int) []testPackRec {
		seen := map[[32]byte]bool{}
		var recs []testPackRec
		for len(recs) < n {
			s := universe[rng.Intn(len(universe))]
			if seen[s] {
				continue
			}
			seen[s] = true
			l := logical[s]
			if clash[s] && rng.Intn(2) == 0 {
				l += 7
			}
			recs = append(recs, fb03Rec(s, l, rng.Intn(4) == 0))
		}
		return recs
	}
	anyTier := func() tier { return tier(rng.Intn(int(numTiers))) }
	for p := 0; p < 60; p++ {
		fb03Pack(t, root, anyTier(), pick(1+rng.Intn(150)))
	}
	fb03Pack(t, root, tierHot, nil) // an empty pack is valid
	for p := 0; p < 4; p++ {
		dict := universe[rng.Intn(len(universe))]
		if p%2 == 1 {
			rng.Read(dict[:])
		}
		fb03Pack(t, root, anyTier(), fb03Preset(rng, dict, pick(2+rng.Intn(40))))
	}
	// A crowd sharing its first 16 bytes in one pack: the repeat check keys
	// on 8 of them, so every entry walks the same probe chain.
	var crowd []testPackRec
	base := universe[rng.Intn(len(universe))]
	for i := 0; i < 64; i++ {
		s := base
		rng.Read(s[16:])
		crowd = append(crowd, fb03Rec(s, 9, false))
	}
	fb03Pack(t, root, tierWarm, crowd)

	// Broken packs, each failing a different check.
	idxStart := func(b []byte, n int) int { return len(b) - packFooterSize - n*packIndexEntrySize }
	p := fb03Pack(t, root, tierHot, pick(20))
	fb03Rewrite(t, p, func(b []byte) []byte { return b[:len(b)/2] })
	p = fb03Pack(t, root, tierWarm, pick(20))
	if err := os.Rename(p, filepath.Join(filepath.Dir(p), locTestPackID(7)+packFileSuffix)); err != nil {
		t.Fatal(err)
	}
	p = fb03Pack(t, root, tierCold, pick(20))
	fb03Rewrite(t, p, func(b []byte) []byte { b[idxStart(b, 20)+5] ^= 1; return b })
	p = fb03Pack(t, root, tierHot, pick(20))
	fb03Rewrite(t, p, func(b []byte) []byte { b[packHeaderSize+packRecordHeaderSize] ^= 1; return b }) // payload: found by verify, not open
	rep := pick(10)
	fb03Pack(t, root, tierHot, append(rep, rep[3]))
	fb03Pack(t, root, tierWarm, append(slices.Clone(crowd[:40]), crowd[17]))
	pre := fb03Preset(rng, universe[0], pick(6))
	pre[2], pre[0] = pre[0], pre[2] // dictionary record out of place
	fb03Pack(t, root, tierHot, pre)
	p = fb03Pack(t, root, tierCold, pick(20))
	fb03Rewrite(t, p, func(b []byte) []byte { // footer claims more records than the file holds
		ft := b[len(b)-packFooterSize:]
		var sha [32]byte
		copy(sha[:], ft[24:56])
		putPackFooter(ft, 1<<40, binary.LittleEndian.Uint64(ft[16:24]), sha, binary.LittleEndian.Uint32(ft[56:60]))
		return b
	})
	name := func(tt tier, id int) string {
		return filepath.Join(tierPackDir(root, tt), locTestPackID(id)+packFileSuffix)
	}
	for _, f := range []struct {
		path string
		data []byte
	}{{name(tierHot, 11), nil}, {name(tierWarm, 12), bytes.Repeat([]byte{0xa5}, 100)}, {filepath.Join(tierPackDir(root, tierHot), "README"), []byte("x")}} {
		if err := os.WriteFile(f.path, f.data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(name(tierCold, 13), 0o755); err != nil {
		t.Fatal(err)
	}
}

// fb03SameState requires two pack states to be identical, level by level.
func fb03SameState(t *testing.T, got, want *packState) {
	t.Helper()
	if !reflect.DeepEqual(got.packs, want.packs) {
		t.Fatalf("packs differ:\n got %+v\nwant %+v", got.packs, want.packs)
	}
	if len(got.bad) != len(want.bad) {
		t.Fatalf("bad packs: got %v want %v", got.bad, want.bad)
	}
	for i := range got.bad {
		if got.bad[i].name != want.bad[i].name || got.bad[i].err.Error() != want.bad[i].err.Error() {
			t.Fatalf("bad pack %d: got %s: %v want %s: %v", i, got.bad[i].name, got.bad[i].err, want.bad[i].name, want.bad[i].err)
		}
	}
	if !slices.Equal(got.clash, want.clash) {
		t.Fatalf("clashes: got %v want %v", got.clash, want.clash)
	}
	if len(got.levels) != len(want.levels) {
		t.Fatalf("levels: got %d want %d", len(got.levels), len(want.levels))
	}
	for i := range got.levels {
		g, w := got.levels[i], want.levels[i]
		if !slices.Equal(g.recs, w.recs) || !slices.Equal(g.dups, w.dups) || !slices.Equal(g.tbl, w.tbl) || g.shift != w.shift || (g.filter == nil) != (w.filter == nil) {
			t.Fatalf("level %d differs: recs %d/%d dups %d/%d", i, len(g.recs), len(w.recs), len(g.dups), len(w.dups))
		}
	}
}

func TestFB03_LocOpenEquivalence(t *testing.T) {
	for seed := int64(1); seed <= 3; seed++ {
		root := t.TempDir()
		fb03Population(t, root, seed)
		for _, format := range []int{storeFormatVersionPacked, storeFormatVersionTiers} {
			want, werr := loadPackStateRef(root, format)
			if werr != nil {
				t.Fatal(werr)
			}
			for _, workers := range []string{"1", "4"} {
				t.Setenv("ZEROS3_MAINT_WORKERS", workers)
				got, err := loadPackState(root, format)
				if err != nil {
					t.Fatal(err)
				}
				fb03SameState(t, got, want)
				for _, ix := range got.levels {
					for _, run := range [][]locRec{ix.recs, ix.dups} {
						for i := range run {
							if run[i].loc().codec == packCodecPresetDict {
								t.Fatalf("a pack-local dictionary record is in the locator: %x", run[i].sum)
							}
						}
					}
				}
			}
			if format < storeFormatVersionTiers {
				continue
			}
			presets := 0
			for _, p := range want.packs {
				if p.presetDict {
					presets++
				}
			}
			if len(want.bad) < 9 || len(want.clash) == 0 || len(want.levels) != 1 || len(want.levels[0].dups) == 0 || presets != 4 {
				t.Fatalf("population too tame: bad %d clash %d levels %d presets %d", len(want.bad), len(want.clash), len(want.levels), presets)
			}
		}
	}
}

// ---- the merge model -------------------------------------------------------

// fb03Model is the locator's meaning without levels: every indexed copy of a
// digest, the retired packs, and the clash reports in publication order.
type fb03Model struct {
	packs  []packInfo
	copies map[[32]byte][]packLoc
	gone   map[int32]bool
	clash  []string
}

func newFB03Model() *fb03Model {
	return &fb03Model{copies: map[[32]byte][]packLoc{}, gone: map[int32]bool{}}
}

func cmpFB03Loc(a, b packLoc) int {
	if c := cmp.Compare(a.tier, b.tier); c != 0 {
		return c
	}
	if c := cmp.Compare(a.pack, b.pack); c != 0 {
		return c
	}
	return cmp.Compare(a.off, b.off)
}

// live is a digest's copies outside retired packs, hottest first, then by
// pack and offset: the primary first.
func (m *fb03Model) live(sum [32]byte) []packLoc {
	var out []packLoc
	for _, l := range m.copies[sum] {
		if !m.gone[l.pack] {
			out = append(out, l)
		}
	}
	slices.SortFunc(out, cmpFB03Loc)
	return out
}

func (m *fb03Model) add(info packInfo, entries []packEntry) {
	idx := int32(len(m.packs))
	m.packs = append(m.packs, info)
	for _, e := range entries {
		if e.codec == packCodecPresetDict {
			continue
		}
		loc := packLoc{pack: idx, tier: info.tier, codec: e.codec, stored: e.stored, logical: e.logical, off: e.off}
		if l := m.live(e.sha); len(l) > 0 && l[0].logical != e.logical {
			m.clash = append(m.clash, fmt.Sprintf("chunk %x has contradictory lengths %d and %d across packs %s and %s",
				e.sha, l[0].logical, e.logical, m.packs[l[0].pack].label(), m.packs[idx].label()))
			continue
		}
		m.copies[e.sha] = append(m.copies[e.sha], loc)
	}
}

// check compares every lookup path of st with the model. Pack order is level
// order when every pack is published whole (strict): then the primary and
// the order of fallbacks are exactly the model's. Otherwise (an open segment
// grew in a newer level than a later pack) only the hottest tier of the
// primary and the set of copies are fixed.
func (m *fb03Model) check(t *testing.T, st *packState, probes [][32]byte, strict bool) {
	t.Helper()
	probe := func(sum [32]byte) {
		want := m.live(sum)
		got, ok := st.lookup(sum)
		if ok != (len(want) > 0) {
			t.Fatalf("lookup %x: found %v, model holds %d copies", sum, ok, len(want))
		}
		if b := st.best(&sum); (b != nil) != ok || ok && b.loc() != got {
			t.Fatalf("best %x disagrees with lookup", sum)
		}
		locs := st.appendLocs(nil, sum)
		if ok && (len(locs) == 0 || locs[0] != got) {
			t.Fatalf("locs %x: primary %+v not first in %+v", sum, got, locs)
		}
		if strict {
			if ok && got != want[0] || !slices.Equal(locs, want) {
				t.Fatalf("%x: lookup %+v locs %+v, want %+v", sum, got, locs, want)
			}
			return
		}
		if ok && got.tier != want[0].tier {
			t.Fatalf("lookup %x: tier %v, hottest copy is %v", sum, got.tier, want[0].tier)
		}
		sorted := slices.SortedFunc(slices.Values(locs), cmpFB03Loc)
		if !slices.Equal(sorted, want) {
			t.Fatalf("locs %x: %+v want %+v", sum, sorted, want)
		}
	}
	n := 0
	for sum := range m.copies {
		probe(sum)
		if len(m.live(sum)) > 0 {
			n++
		}
	}
	for _, sum := range probes {
		probe(sum)
	}
	seen := map[[32]byte]bool{}
	for sum, loc := range st.primaries() {
		if seen[*sum] {
			t.Fatalf("primaries repeats %x", *sum)
		}
		seen[*sum] = true
		if want := m.live(*sum); len(want) == 0 || strict && loc != want[0] || loc.tier != want[0].tier {
			t.Fatalf("primaries %x: %+v want %+v", *sum, loc, want)
		}
	}
	if len(seen) != n || st.distinct() != n {
		t.Fatalf("primaries %d distinct %d want %d", len(seen), st.distinct(), n)
	}
	if !slices.Equal(st.clash, m.clash) {
		t.Fatalf("clashes:\n got %q\nwant %q", st.clash, m.clash)
	}
}

// fb03CheckFilters requires every level but the oldest to carry a filter
// that admits each digest the level holds.
func fb03CheckFilters(t *testing.T, st *packState) {
	t.Helper()
	for k, ix := range st.levels {
		if ix.filter == nil {
			if k > 0 {
				t.Fatalf("level %d of %d has no filter", k, len(st.levels))
			}
			continue
		}
		for _, run := range [][]locRec{ix.recs, ix.dups} {
			for i := range run {
				if !ix.filter.has(&run[i].sum) {
					t.Fatalf("level %d filter rejects a digest it holds: %x", k, run[i].sum)
				}
			}
		}
	}
}

// fb03CheckSettled requires the shape a quiet locator has: at most
// locMaxLevels levels, each locRatio times the next, no level made mostly of
// retired records, and none of the levels a merge produced (those not in
// before) holding any.
func fb03CheckSettled(t *testing.T, st *packState, before []*locIndex) {
	t.Helper()
	if len(st.levels) > locMaxLevels {
		t.Fatalf("%d levels after settling", len(st.levels))
	}
	total := 0
	for _, ix := range st.levels {
		total += ix.size()
	}
	r := locRatio(total, locMaxLevels)
	for k := 1; k < len(st.levels); k++ {
		if r*st.levels[k].size() >= st.levels[k-1].size() {
			t.Fatalf("levels %d and %d break ratio %d: %d and %d records", k-1, k, r, st.levels[k-1].size(), st.levels[k].size())
		}
	}
	old := map[*locIndex]bool{}
	for _, ix := range before {
		old[ix] = true
	}
	for k, ix := range st.levels {
		g := st.goneIn(ix)
		if !old[ix] && g != 0 {
			var ex []string
			for _, run := range [][]locRec{ix.recs, ix.dups} {
				for i := range run {
					if st.isGone(run[i].packIdx()) && len(ex) < 4 {
						ex = append(ex, fmt.Sprintf("pack %d tier %v gone-bitmap-len %d", run[i].packIdx(), run[i].tier(), len(st.gone)))
					}
				}
			}
			t.Fatalf("merged level %d kept %d retired records: %v", k, g, ex)
		}
		if ix.size() >= locPurgeMinRec && 4*g >= ix.size() {
			t.Fatalf("level %d is %d/%d retired records", k, g, ix.size())
		}
	}
	fb03CheckFilters(t, st)
}

// fb03Sums draws n distinct digests from universe.
func fb03Sums(rng *rand.Rand, universe [][32]byte, n int) [][32]byte {
	seen := map[[32]byte]bool{}
	out := make([][32]byte, 0, n)
	for len(out) < n {
		s := universe[rng.Intn(len(universe))]
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func fb03Universe(rng *rand.Rand, extra int) [][32]byte {
	u := locTestUniverse(rng)
	for i := 0; i < extra; i++ {
		var s [32]byte
		rng.Read(s[:])
		u = append(u, s)
	}
	return u
}

// TestFB03_LocMergeModel publishes, retires and settles at random and holds
// every lookup path to the model after each step (G-LINEAR, G-MODEL): merges
// keep the meaning of the locator, drop retired records, and settle to the
// level bound.
func TestFB03_LocMergeModel(t *testing.T) {
	seeds := int64(4)
	if testing.Short() {
		seeds = 2
	}
	for seed := int64(1); seed <= seeds; seed++ {
		rng := rand.New(rand.NewSource(seed))
		universe := fb03Universe(rng, 30000)
		probes := locTestProbes(rng, universe)
		logical := map[[32]byte]uint32{}
		for _, s := range universe {
			logical[s] = uint32(1 + rng.Intn(1000))
		}
		m := newFB03Model()
		st := &packState{}
		settles, purged := 0, 0
		for op := 0; op < 300; op++ {
			switch x := rng.Intn(100); {
			case x < 60:
				n := 1 + rng.Intn(64)
				if rng.Intn(5) == 0 {
					n = 1000 + rng.Intn(8000)
				}
				sums := fb03Sums(rng, universe, n)
				entries := locTestEntries(sums, func(i int) uint32 {
					if rng.Intn(200) == 0 {
						return logical[sums[i]] + 3 // contradicts the indexed copy
					}
					return logical[sums[i]]
				}, func(int) bool { return rng.Intn(3) == 0 })
				info := packInfo{id: locTestPackID(op), tier: tier(rng.Intn(int(numTiers))), records: len(entries)}
				next, err := st.withPack(info, entries)
				if err != nil {
					t.Fatal(err)
				}
				st = next
				m.add(info, entries)
			case x < 75:
				var idx []int32
				for k := 0; k < 1+rng.Intn(6) && len(st.packs) > 0; k++ {
					if i := int32(rng.Intn(len(st.packs))); !m.gone[i] {
						idx = append(idx, i)
						m.gone[i] = true
					}
				}
				st = st.withoutPacks(idx)
			default:
				before := st.levels
				st = st.settled()
				fb03CheckSettled(t, st, before)
				settles++
				for _, ix := range before {
					if st.goneIn(ix) > 0 && !slices.Contains(st.levels, ix) {
						purged++
					}
				}
			}
			if op%10 == 9 || op == 299 {
				m.check(t, st, probes, true)
			}
			fb03CheckFilters(t, st)
		}
		before := st.levels
		st = st.settled()
		fb03CheckSettled(t, st, before)
		m.check(t, st, probes, true)
		total := 0
		for _, ix := range st.levels {
			total += ix.size()
		}
		t.Logf("seed %d: %d packs, %d records in %d levels, %d settles, %d levels with retired records rewritten", seed, len(st.packs), total, len(st.levels), settles, purged)
		if purged == 0 {
			t.Fatal("no retired records were ever merged away")
		}
	}
}

// TestFB03_LocMergeSegments grows open segments (records of an earlier pack
// published in a newer level than later packs) among whole packs: copies,
// tiers and absence stay exact, which is what the failure criterion asks.
func TestFB03_LocMergeSegments(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	universe := fb03Universe(rng, 5000)
	probes := locTestProbes(rng, universe)
	m := newFB03Model()
	st := &packState{}
	type seg struct {
		idx  int32
		next uint64
		used map[[32]byte]bool
	}
	var segs []*seg
	for op := 0; op < 400; op++ {
		switch x := rng.Intn(100); {
		case x < 15 || len(segs) == 0:
			info := packInfo{id: locTestPackID(1<<20 + op), tier: tierHot, open: true}
			next, err := st.withPack(info, nil)
			if err != nil {
				t.Fatal(err)
			}
			st = next
			m.add(info, nil)
			segs = append(segs, &seg{idx: int32(len(st.packs) - 1), next: packHeaderSize + packRecordHeaderSize, used: map[[32]byte]bool{}})
		case x < 55:
			g := segs[rng.Intn(len(segs))]
			if m.gone[g.idx] {
				continue
			}
			var entries []packEntry
			for _, s := range fb03Sums(rng, universe, 1+rng.Intn(40)) {
				if g.used[s] {
					continue
				}
				g.used[s] = true
				entries = append(entries, packEntry{sha: s, off: g.next, stored: 1, logical: 1})
				g.next += 1 + packRecordHeaderSize
			}
			next, err := st.withRecords(uint32(g.idx), tierHot, entries, nil)
			if err != nil {
				t.Fatal(err)
			}
			st = next
			idx := g.idx
			for _, e := range entries {
				m.copies[e.sha] = append(m.copies[e.sha], packLoc{pack: idx, tier: tierHot, codec: e.codec, stored: 1, logical: 1, off: e.off})
			}
		case x < 80:
			sums := fb03Sums(rng, universe, 1+rng.Intn(300))
			entries := locTestEntries(sums, func(int) uint32 { return 1 }, func(int) bool { return false })
			info := packInfo{id: locTestPackID(op), tier: tier(rng.Intn(int(numTiers))), records: len(entries)}
			next, err := st.withPack(info, entries)
			if err != nil {
				t.Fatal(err)
			}
			st = next
			m.add(info, entries)
		case x < 88:
			i := int32(rng.Intn(len(st.packs)))
			if !m.gone[i] {
				m.gone[i] = true
				st = st.withoutPacks([]int32{i})
			}
		default:
			before := st.levels
			st = st.settled()
			fb03CheckSettled(t, st, before)
		}
		if op%20 == 19 {
			m.check(t, st, probes, false)
		}
	}
	m.check(t, st, probes, false)
}

// TestFB03_LocMergeLinear holds the linear k-way merge to the sort-based
// build it replaces: identical records, fallbacks, table, filter and clash
// reports, with and without retired packs.
func TestFB03_LocMergeLinear(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	universe := fb03Universe(rng, 4000)
	for trial := 0; trial < 60; trial++ {
		var levels []*locIndex
		var all []locRec
		pack := uint32(0)
		for l := 0; l < 1+rng.Intn(6); l++ {
			var recs []locRec
			for p := 0; p < 1+rng.Intn(4); p++ {
				tt := tier(rng.Intn(int(numTiers)))
				for _, s := range fb03Sums(rng, universe, rng.Intn(400)) {
					e := packEntry{sha: s, stored: 1, logical: 1 + uint32(rng.Intn(2)), off: uint64(rng.Intn(1 << 30))}
					recs = append(recs, mkLocRec(&e, pack, tt))
				}
				pack++
			}
			ix := buildLocIndex(recs, func(prim, rep *locRec) {})
			levels = append(levels, ix)
			all = append(append(all, ix.recs...), ix.dups...)
		}
		retired := map[uint32]bool{}
		for p := uint32(0); p < pack; p++ {
			if trial%2 == 1 && rng.Intn(3) == 0 {
				retired[p] = true
			}
		}
		var gone func(*locRec) bool
		if len(retired) > 0 {
			gone = func(r *locRec) bool { return retired[r.packIdx()] }
		}
		kept := slices.DeleteFunc(slices.Clone(all), func(r locRec) bool { return retired[r.packIdx()] })
		var wantClash, gotClash [][2]locRec
		want := finishLocIndex(buildLocIndex(kept, func(prim, rep *locRec) { wantClash = append(wantClash, [2]locRec{*prim, *rep}) }), true)
		got := mergeLevels(levels, gone, true, func(prim, rep *locRec) { gotClash = append(gotClash, [2]locRec{*prim, *rep}) })
		if !slices.Equal(got.recs, want.recs) || !slices.Equal(got.dups, want.dups) || !slices.Equal(got.tbl, want.tbl) || got.shift != want.shift {
			t.Fatalf("trial %d: merge differs from a rebuild", trial)
		}
		if (got.filter == nil) != (want.filter == nil) || got.filter != nil && !slices.Equal(got.filter.blocks, want.filter.blocks) {
			t.Fatalf("trial %d: filters differ", trial)
		}
		if !slices.Equal(gotClash, wantClash) {
			t.Fatalf("trial %d: clashes differ: %d vs %d", trial, len(gotClash), len(wantClash))
		}
	}
}

// ---- forced collisions (G-COLLISION) ---------------------------------------

// TestFB03_LocForcedCollision: digests sharing their table bucket and their
// first 16 bytes (the open repeat check's key, the filter's block and bits)
// and differing only after, plus X2's forgeries of present digests (two
// trailing bytes changed), must be answered exactly by every consumer: the
// open, lookup, best, appendLocs, primaries and distinct, on one level, on
// unmerged filtered levels, after merges and after a retirement. No lookup
// may return a location for a digest it does not hold.
func TestFB03_LocForcedCollision(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	var base [32]byte
	rng.Read(base[:])
	crowd := make([][32]byte, 96)
	for i := range crowd {
		s := base
		rng.Read(s[16:])
		crowd[i] = s
	}
	present, absent := crowd[:64], slices.Clone(crowd[64:])
	for _, s := range present[:16] {
		f := s
		f[31] ^= 0x5a
		f[20] ^= 0x01
		absent = append(absent, f)
	}
	recs := func(ss [][32]byte) []testPackRec {
		var out []testPackRec
		for _, s := range ss {
			out = append(out, fb03Rec(s, 5, false))
		}
		return out
	}
	root := t.TempDir()
	fb03PackDirs(t, root)
	fb03Pack(t, root, tierHot, recs(present[0:24]))
	fb03Pack(t, root, tierHot, recs(present[16:48])) // copies of 16..23
	fb03Pack(t, root, tierWarm, recs(present[40:64]))
	fb03Pack(t, root, tierCold, recs(present[0:8]))
	bad := fb03Pack(t, root, tierHot, append(recs(crowd[:40]), recs(crowd[30:31])...))
	opened, err := loadPackState(root, storeFormatVersionTiers)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.bad) != 1 || !strings.Contains(opened.bad[0].err.Error(), "repeats chunk") || !strings.HasSuffix(bad, opened.bad[0].name) {
		t.Fatalf("the true repeat behind 40 same-key entries was not caught: %v", opened.bad)
	}
	m := newFB03Model()
	var entries [][]packEntry
	for _, p := range opened.packs {
		_, es, err := loadPackFile(p.path)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, es)
		m.add(p, es)
	}
	m.check(t, opened, absent, true)
	ref, err := loadPackStateRef(root, storeFormatVersionTiers)
	if err != nil {
		t.Fatal(err)
	}
	fb03SameState(t, opened, ref)

	st := &packState{}
	for i, p := range opened.packs {
		if st, err = st.withPack(p, entries[i]); err != nil {
			t.Fatal(err)
		}
	}
	if len(st.levels) < 2 || st.levels[1].filter == nil || !st.levels[1].filter.has(&absent[0]) {
		t.Fatal("the crowd did not force the filter")
	}
	m.check(t, st, absent, true)
	st = st.settled()
	m.check(t, st, absent, true)
	st = st.withoutPacks([]int32{1})
	m.gone[1] = true
	m.check(t, st, absent, true)
	st = st.settled()
	m.check(t, st, absent, true)
}

// ---- the background merger ------------------------------------------------

func fb03FreshSums(rng *rand.Rand, n int) [][32]byte {
	out := make([][32]byte, n)
	for i := range out {
		rng.Read(out[i][:])
	}
	return out
}

func fb03AddPack(t *testing.T, s *Store, id int, sums [][32]byte) {
	t.Helper()
	entries := locTestEntries(sums, func(int) uint32 { return 1 }, func(int) bool { return false })
	if err := s.addPack(packInfo{id: locTestPackID(id), tier: tierHot, records: len(entries)}, entries); err != nil {
		t.Fatal(err)
	}
}

// TestFB03_LocBackgroundMerge ingests through Store.addPack while readers
// look up published and absent digests on whatever snapshot they load: the
// level count stays bounded, a retirement is merged away, a publish made
// during a background merge survives its swap, and a merge that raced with a
// reload is discarded.
func TestFB03_LocBackgroundMerge(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rng := rand.New(rand.NewSource(17))
	packs, per := 2000, 150
	if testing.Short() {
		packs, per = 400, 400
	}
	sums := make([][][32]byte, packs)
	for i := range sums {
		sums[i] = fb03FreshSums(rng, per)
	}
	var published, wrong, reads atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func(rr *rand.Rand) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				n := int(published.Load())
				if n == 0 {
					continue
				}
				st := s.packSnap()
				if _, ok := st.lookup(sums[rr.Intn(n)][rr.Intn(per)]); !ok {
					wrong.Add(1)
				}
				var a [32]byte
				rr.Read(a[:])
				if _, ok := st.lookup(a); ok {
					wrong.Add(1)
				}
				reads.Add(2)
			}
		}(rand.New(rand.NewSource(int64(100 + r))))
	}
	maxLevels := 0
	for i := range packs {
		fb03AddPack(t, s, i, sums[i])
		published.Store(int64(i + 1))
		maxLevels = max(maxLevels, len(s.packSnap().levels))
	}
	close(stop)
	wg.Wait()
	s.locMergeWait()
	st := s.packSnap()
	stats := &s.loc.stats
	t.Logf("%d records: %d levels at the end, at most %d seen (%d recorded); %d merges, max merge %.1f ms, max swap %.1f us; %d reads",
		packs*per, len(st.levels), maxLevels, stats.MaxLevels.Load(), stats.Merges.Load(),
		float64(stats.MaxMergeNS.Load())/1e6, float64(stats.MaxSwapNS.Load())/1e3, reads.Load())
	if wrong.Load() != 0 {
		t.Fatalf("%d lookups gave the wrong answer during merges", wrong.Load())
	}
	if stats.Merges.Load() == 0 || maxLevels > locMaxLevels+2 || st.needsMerge(nil) {
		t.Fatalf("merges %d, max levels %d, work left %v", stats.Merges.Load(), maxLevels, st.needsMerge(nil))
	}
	fb03CheckSettled(t, st, nil)
	if st.distinct() != packs*per {
		t.Fatalf("distinct %d want %d", st.distinct(), packs*per)
	}

	// Retire every other pack: the purge rewrites each large level.
	var idx []int32
	for i := 0; i < packs; i += 2 {
		idx = append(idx, int32(i))
	}
	s.packMu.Lock()
	s.locPublished(s.packSnap().withoutPacks(idx))
	s.packMu.Unlock()
	s.loc.purge.Store(true)
	s.locMergeKick()
	s.locMergeWait()
	st = s.packSnap()
	total := 0
	for k, ix := range st.levels {
		total += ix.size()
		if g := st.goneIn(ix); ix.size() >= locPurgeMinRec && 4*g >= ix.size() {
			t.Fatalf("level %d still holds %d retired records", k, g)
		}
	}
	if stats.Purges.Load() == 0 || total > packs*per/2+len(st.levels)*locPurgeMinRec {
		t.Fatalf("purges %d, %d records left for %d live", stats.Purges.Load(), total, packs*per/2)
	}
	for i := range packs {
		if _, ok := st.lookup(sums[i][i%per]); ok != (i%2 == 1) {
			t.Fatalf("pack %d: found %v after retiring every other pack", i, ok)
		}
	}

	// A publish made while a background merge runs survives the swap.
	extra := packs
	freeze := func(st *packState) map[*locIndex]bool {
		f := map[*locIndex]bool{}
		for _, ix := range st.levels {
			f[ix] = true
		}
		return f
	}
	s.packMu.Lock()
	cur := s.packSnap()
	for k := 0; k < locMaxLevels+2; k++ {
		entries := locTestEntries(fb03FreshSums(rng, 3*locSyncMerge), func(int) uint32 { return 1 }, func(int) bool { return false })
		if cur, err = cur.withPackF(packInfo{id: locTestPackID(extra), tier: tierHot, records: len(entries)}, entries, freeze(cur)); err != nil {
			t.Fatal(err)
		}
		extra++
	}
	s.packSt.Store(cur)
	s.packMu.Unlock()
	during := fb03FreshSums(rng, 50)
	var fired atomic.Int32
	defer func() { testHook = nil }()
	testHook = func(point string) {
		if point == hookLocMergeBeforePublish && fired.Add(1) == 1 {
			fb03AddPack(t, s, extra, during)
		}
	}
	merges := stats.Merges.Load()
	if !s.locMergeOnce() || stats.Merges.Load() < merges+1 {
		t.Fatal("the planned merge did not publish")
	}
	s.locMergeWait()
	for _, d := range during {
		if _, ok := s.packSnap().lookup(d); !ok {
			t.Fatal("a pack published during a background merge was lost by its swap")
		}
	}

	// A merge that raced with a reload is discarded.
	s.packMu.Lock()
	cur = s.packSnap()
	for k := 0; k < locMaxLevels+2; k++ {
		entries := locTestEntries(fb03FreshSums(rng, 1000), func(int) uint32 { return 1 }, func(int) bool { return false })
		if cur, err = cur.withPackF(packInfo{id: locTestPackID(extra + 1 + k), tier: tierHot, records: len(entries)}, entries, freeze(cur)); err != nil {
			t.Fatal(err)
		}
	}
	s.packSt.Store(cur)
	s.packMu.Unlock()
	fired.Store(0)
	testHook = func(point string) {
		if point == hookLocMergeBeforePublish && fired.Add(1) == 1 {
			if err := s.reloadPacks(); err != nil {
				t.Error(err)
			}
		}
	}
	discarded := stats.Discarded.Load()
	s.locMergeOnce()
	if stats.Discarded.Load() < discarded+1 || len(s.packSnap().packs) != 0 {
		t.Fatalf("a merge of a reloaded locator was published: discarded %d, %d packs", stats.Discarded.Load()-discarded, len(s.packSnap().packs))
	}
}
