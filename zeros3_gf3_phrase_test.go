package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"os"
	"slices"
	"testing"
	"time"
)

// Mine repeat extents from several complete objects, then cluster by
// co-use when placing them into independently readable source pages.
type gf3Location struct{ object, pos int }
type gf3Phrase struct {
	raw    []byte
	uses   []gf3Location
	owners uint16
	score  int
	digest [32]byte
}

const (
	gf3PhraseMaximum = 3072
	gf3PhraseBudget  = 384 << 10
	gf3PhrasePages   = 16 << 10
)

func gf3PhraseAnchors(objects [][]byte) map[uint64][]gf3Location {
	const span = 32
	const limit = 1 << 17
	anchors := make(map[uint64][]gf3Location)
	for object, raw := range objects {
		if len(raw) < span {
			continue
		}
		hash := dwDcore_rollInit(raw[:span])
		for pos := 0; ; pos++ {
			// Independent of object offsets. Select about one seed per 128
			// random windows, with a hard cap on the number of distinct tags.
			key := dwDcore_mix64(hash)
			if key&127 == 0 {
				hits, exists := anchors[key]
				if exists || len(anchors) < limit {
					if len(hits) == 0 || hits[len(hits)-1].object != object {
						anchors[key] = append(hits, gf3Location{object, pos})
					}
				}
			}
			if pos+span == len(raw) {
				break
			}
			hash = dwDcore_rollNext(hash, raw[pos], raw[pos+span], span)
		}
	}
	return anchors
}

func gf3MakePhrase(objects [][]byte, hits []gf3Location) gf3Phrase {
	const span = 32
	first := hits[0]
	base := objects[first.object]
	for _, h := range hits[1:] {
		if !bytes.Equal(base[first.pos:first.pos+span], objects[h.object][h.pos:h.pos+span]) {
			return gf3Phrase{}
		}
	}
	left, right := 0, span
	for left < gf3PhraseMaximum/2 {
		equal := true
		for _, h := range hits {
			if h.pos-left <= 0 || objects[h.object][h.pos-left-1] != base[first.pos-left-1] {
				equal = false
				break
			}
		}
		if !equal {
			break
		}
		left++
	}
	for left+right < gf3PhraseMaximum {
		equal := true
		for _, h := range hits {
			if h.pos+right >= len(objects[h.object]) || objects[h.object][h.pos+right] != base[first.pos+right] {
				equal = false
				break
			}
		}
		if !equal {
			break
		}
		right++
	}
	if len(hits) < 2 || left+right < 192 {
		return gf3Phrase{}
	}
	raw := base[first.pos-left : first.pos+right]
	phrase := gf3Phrase{raw: raw, digest: sha256.Sum256(raw)}
	for _, h := range hits {
		phrase.uses = append(phrase.uses, gf3Location{h.object, h.pos - left})
		phrase.owners |= 1 << h.object
	}
	// Admission rechecks approximate exclusive savings after overlap.
	phrase.score = (len(phrase.uses) - 1) * len(raw)
	return phrase
}

func gf3MinePhrases(objects [][]byte, budget int) []gf3Phrase {
	if len(objects) < 2 || len(objects) > 16 {
		panic("GF3 phrase bank bounds")
	}
	anchors := gf3PhraseAnchors(objects)
	unique := map[[32]byte]gf3Phrase{}
	// An intentionally bounded deterministic anchor pass. High-frequency
	// repeated content consumes the same fixed allowance as other data.
	var keys []uint64
	for key, hits := range anchors {
		if len(hits) >= 2 {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, b uint64) int {
		return cmp.Or(cmp.Compare(len(anchors[b]), len(anchors[a])), cmp.Compare(a, b))
	})
	for _, key := range keys[:min(len(keys), 8192)] {
		phrase := gf3MakePhrase(objects, anchors[key])
		if phrase.score > unique[phrase.digest].score {
			unique[phrase.digest] = phrase
		}
	}
	all := make([]gf3Phrase, 0, len(unique))
	for _, p := range unique {
		all = append(all, p)
	}
	slices.SortFunc(all, func(a, b gf3Phrase) int {
		return cmp.Or(cmp.Compare(b.score, a.score), bytes.Compare(a.digest[:], b.digest[:]))
	})
	occupied := make([][]bool, len(objects))
	for i, raw := range objects {
		occupied[i] = make([]bool, (len(raw)+63)/64)
	}
	var selected []gf3Phrase
	total := 0
	for _, p := range all {
		if len(p.raw) > budget-total {
			continue
		}
		var free []gf3Location
		for _, v := range p.uses {
			cells := occupied[v.object][v.pos/64 : (v.pos+len(p.raw)+63)/64]
			if !slices.Contains(cells, true) {
				free = append(free, v)
			}
		}
		if len(free) < 2 {
			continue
		}
		// Charge the actual incumbent pack codec for the source. A generic
		// logical coverage heuristic otherwise rewards highly compressible
		// repeated blanks whose unique physical charge was nearly zero.
		priced := int(packPriceLen(p.raw))
		if (len(free)-1)*priced < packRecordBytes+len(free)*48 {
			continue
		}
		p.uses = free
		p.owners = 0
		for _, v := range free {
			p.owners |= 1 << v.object
			cells := occupied[v.object][v.pos/64 : (v.pos+len(p.raw)+63)/64]
			for i := range cells {
				cells[i] = true
			}
		}
		total += len(p.raw)
		selected = append(selected, p)
	}
	return selected
}

type gf3Page struct {
	raw    []byte
	owners uint16
	ref    repMember
}

func gf3PackBank(phrases []gf3Phrase, locality bool) []gf3Page {
	phrases = slices.Clone(phrases)
	slices.SortFunc(phrases, func(a, b gf3Phrase) int {
		if locality {
			if order := cmp.Compare(a.owners, b.owners); order != 0 {
				return order
			}
		}
		return bytes.Compare(a.digest[:], b.digest[:])
	})
	var pages []gf3Page
	for _, p := range phrases {
		n := len(pages)
		if n == 0 || len(p.raw)+len(pages[n-1].raw) > gf3PhrasePages {
			pages = append(pages, gf3Page{})
			n++
		}
		page := &pages[n-1]
		page.raw = append(page.raw, p.raw...)
		page.owners |= p.owners
	}
	return pages
}

func gf3ControlledCohort() [][]byte {
	const members = 9
	const phrases = 23
	dictionary := make([][]byte, phrases)
	for i := range dictionary {
		dictionary[i] = genRandomBytes(int64(31201+i*1121), 1200+(i*211)%2000)
	}
	out := make([][]byte, members)
	for i := range out {
		for j := range phrases {
			phrase := (j*7 + i*5) % phrases
			if (phrase+i)%5 == 0 {
				continue
			}
			out[i] = append(out[i], genRandomBytes(int64(99213+i*521+j), 300+(29*j+i*16)%921)...)
			out[i] = append(out[i], dictionary[phrase]...)
		}
		out[i] = append(out[i], genRandomBytes(int64(91637+i), 9000+i*511)...)
	}
	return out
}

// Store derived pages as verified CAS records. A page remains reachable
// only while a representation names it. Whole reads prove every target.
func gf3ApplyBank(t *testing.T, l *authLab, pages []gf3Page, input [][]byte) (used, rewrites int) {
	t.Helper()
	batch := l.s.newCASBatch()
	defer batch.Abort()
	for i := range pages {
		p := &pages[i]
		sum, err := batch.Add(p.raw)
		mustOK(t, err)
		p.ref = repMember{Role: repStored, SHA: sum, Size: int64(len(p.raw))}
	}
	mustOK(t, batch.Flush())
	mustOK(t, l.s.ensureFeature(agFeature))
	cfg := gf2MatchConfig{"phrase", 1, 4, 16, true, true}
	referenced := map[[32]byte]bool{}
	for i, raw := range input {
		var source []byte
		var members []repMember
		// Assign only pages whose phrases supplied a verified exact match
		// during mining. A page can still hold bytes shared by other objects.
		for _, p := range pages {
			if p.owners&(1<<i) == 0 {
				continue
			}
			source = append(source, p.raw...)
			members = append(members, p.ref)
		}
		if len(source) == 0 {
			continue
		}
		man := l.man(fmt.Sprintf("series/%02d", i))
		copied, encoded, err := gf2PrepareNativePatch(members, source, raw, cfg)
		mustOK(t, err)
		// Compare exclusive bytes at the native record price. This is a
		// necessary gate. Physical bank and metadata charges are measured later.
		if copied == 0 || encoded.ownBytes() >= int64(packPriceLen(raw)) {
			continue
		}
		_, err = gf2CommitNativePatch(l.s, man, raw, encoded)
		mustOK(t, err)
		rewrites++
		for _, m := range members {
			referenced[m.SHA] = true
		}
	}
	for _, p := range pages {
		if referenced[p.ref.SHA] {
			used += len(p.raw)
		}
	}
	return used, rewrites
}

func gf3NativePhraseCase(t *testing.T, family string, input [][]byte, budget int, lanes []string) {
	t.Helper()
	before, cpu := time.Now(), fb13CPU()
	bank := gf3MinePhrases(input, budget)
	miningCPU, miningWall := int64(fb13CPU()-cpu), time.Since(before).Nanoseconds()
	logical := 0
	var roots int
	for _, raw := range input {
		logical += len(raw)
	}
	for _, row := range bank {
		roots += bits.OnesCount16(row.owners)
	}
	for _, lane := range lanes {
		t.Run(lane, func(t *testing.T) {
			l := newAuthLab(t)
			l.s.writeBinaryManifests = true
			for i, raw := range input {
				l.put(fmt.Sprintf("series/%02d", i), raw)
			}
			ctx := context.Background()
			_, err := l.s.finishAuthorityArchive(ctx, 0)
			mustOK(t, err)
			used, rewritten := 0, 0
			var bankPacked int64
			var madePages []gf3Page
			before, cpu = time.Now(), fb13CPU()
			switch lane {
			case "archive", "single-xd", "multi-xd":
				mustOK(t, l.s.ensureFeature(agFeature))
				archive := lane == "archive"
				policy := OptimizePolicy{
					AuthPolicy: AuthPolicy{Apply: true, Order: "oldest", AllowDelta: true, AllowRep: archive, CodecReplay: archive, MaxDepth: 3},
					Profile:    "archive", Canonical: archive, Splice: archive, SparseFiles: true, FastReencode: true,
					Region: rsDefaultParams(),
				}
				policy.Region.Multi = lane != "single-xd"
				if policy.Region.Multi {
					policy.MaxBases = 4
				} else {
					policy.MaxBases = 1
				}
				rep, err := l.s.AuthorityOptimize(ctx, policy)
				mustOK(t, err)
				rewritten = rep.Applied
			case "phrase-digest", "phrase-locality":
				madePages = gf3PackBank(bank, lane == "phrase-locality")
				used, rewritten = gf3ApplyBank(t, l, madePages, input)
			}

			planCPU, planWall := int64(fb13CPU()-cpu), time.Since(before).Nanoseconds()
			_, err = l.s.finishAuthorityArchive(ctx, 0)
			mustOK(t, err)
			l.close()
			total, packs, _ := fb13Allocation(t, l.dir)
			l.open()
			for _, page := range madePages {
				if loc, ok := l.s.packLookup(page.ref.SHA); ok {
					bankPacked += int64(loc.stored) + packRecordBytes
				}
			}
			var rangeNS, wholeNS, rangeBytes, rangePacks []int64
			for i, raw := range input {
				name := fmt.Sprintf("series/%02d", i)
				before = time.Now()
				got := l.read(name, byteRange{0, int64(len(raw)) - 1})
				wholeNS = append(wholeNS, time.Since(before).Nanoseconds())
				if !bytes.Equal(got, raw) {
					t.Fatal("phrase whole-object replay mismatch")
				}
				for j := range 9 {
					n := min(len(raw), 4096)
					off := int64((len(raw) - n) * j / 8)
					before = time.Now()
					got = l.read(name, byteRange{off, off + int64(n) - 1})
					rangeNS = append(rangeNS, time.Since(before).Nanoseconds())
					if madePages != nil {
						stored, packs := gf3PhraseReadCost(t, l, name, off, int64(n))
						rangeBytes = append(rangeBytes, stored)
						rangePacks = append(rangePacks, packs)
					}
					if !bytes.Equal(got, raw[off:off+int64(n)]) {
						t.Fatal("phrase sparse replay mismatch")
					}
				}
			}
			l.verifyOK("native phrase experiment")
			row, err := json.Marshal(map[string]any{"family": family, "lane": lane, "allocated": total, "packs": packs,
				"logical_bytes": logical, "root_count": len(input), "bank_source_logical_bytes": used, "bank_record_bytes": bankPacked, "rewrites": rewritten,
				"phrases": len(bank), "phrase_subscriptions": roots, "bank_budget": budget, "mining_cpu_ns": miningCPU, "mining_wall_ns": miningWall,
				"plan_cpu_ns": planCPU, "plan_wall_ns": planWall,
				"whole_p50_p95_p99_ns": gf0Percentiles(wholeNS), "range_p50_p95_p99_ns": gf0Percentiles(rangeNS),
				"phrase_range_stored_bytes_p95": fb14Quantile(rangeBytes, 95), "phrase_range_pack_count_p95": fb14Quantile(rangePacks, 95)})
			mustOK(t, err)
			fmt.Printf("GF3PHRASE %s\n", row)
			if madePages != nil && family == "controlled-cohort" {
				retained, released := gf3PruneBank(t, l, input, madePages)
				fmt.Printf(`GF3PRUNE {"lane":%q,"after_half":%d,"after_all":%d}`+"\n", lane, retained, released)
			}
		})
	}
}

func TestGF3NativePhrasePanel(t *testing.T) {
	lanes := []string{"cdc", "single-xd", "multi-xd", "archive", "phrase-digest", "phrase-locality"}
	t.Run("controlled-cohort", func(t *testing.T) {
		gf3NativePhraseCase(t, "controlled-cohort", gf3ControlledCohort(), gf3PhraseBudget, lanes)
	})
	root, families := gf0Corpus(t)
	for _, fam := range families {
		if !slices.Contains([]string{"sqlite", "parquet", "httpx-tar", "entropy"}, fam.Name) {
			continue
		}
		t.Run(fam.Name, func(t *testing.T) {
			var input [][]byte
			for i := range fam.Paths {
				data, _ := gf0ReadFixture(t, root, fam, i)
				input = append(input, data)
			}
			gf3NativePhraseCase(t, fam.Name, input, gf3PhraseBudget, lanes)
		})
	}
}

func TestGF3SmallBankSweep(t *testing.T) {
	root, families := gf0Corpus(t)
	for _, fam := range families {
		if fam.Name != "sqlite" && fam.Name != "parquet" {
			continue
		}
		t.Run(fam.Name, func(t *testing.T) {
			var input [][]byte
			for i := range fam.Paths {
				data, _ := gf0ReadFixture(t, root, fam, i)
				input = append(input, data)
			}
			for _, budget := range []int{64 << 10, 128 << 10, 256 << 10} {
				t.Run(fmt.Sprint(budget), func(t *testing.T) { gf3NativePhraseCase(t, fam.Name, input, budget, []string{"phrase-locality"}) })
			}
		})
	}
}

// Independent telemetry over the actual verified native source. Read the
// same range as the measured request, with a fresh source and no cache.
func gf3PhraseReadCost(t *testing.T, l *authLab, key string, off, n int64) (int64, int64) {
	t.Helper()
	sum, err := decodeHexSHA256(l.man(key).ObjectSHA256)
	mustOK(t, err)
	rec, err := l.s.authLoad(sum)
	mustOK(t, err)
	if rec == nil {
		return 0, 0
	}
	src := l.s.newRepSource(rec.Root.Descs, nil)
	defer src.close()
	env := &repEnv{Src: src, Limits: repProdLimits}
	call, err := env.open(rec.Root.Rep)
	mustOK(t, err)
	read, err := repRangeReader(call, off, n)
	mustOK(t, err)
	if read == nil {
		t.Fatal("phrase representation has no native range path")
	}
	mustOK(t, read(io.Discard))
	return src.stats.StoredBytes, int64(len(src.stats.Packs))
}

func gf3PruneBank(t *testing.T, l *authLab, input [][]byte, pages []gf3Page) (int, int) {
	t.Helper()
	count := func() int {
		n := 0
		for _, p := range pages {
			if _, err := l.s.casStat(p.ref.SHA); err == nil {
				n++
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("phrase lookup: %v", err)
			}
		}
		return n
	}
	finalize := func() {
		// Delete leaves retained history. Expire those roots before asking
		// canonical maintenance to free their data.
		l.offline(func() {
			_, err := pruneHistoryStore(l.dir, historyPruneOptions{Bucket: "b", KeepLast: pruneKeep(0)}, true)
			mustOK(t, err)
		})
		_, err := l.s.finishAuthorityArchive(context.Background(), 0)
		mustOK(t, err)
	}
	for i := range input {
		if i%2 != 0 {
			continue
		}
		key := fmt.Sprintf("series/%02d", i)
		mustOK(t, l.s.DeleteObject("b", key))
		delete(l.want, key)
	}
	finalize()
	l.exact("phrase cohort pruning")
	retained := count()
	if retained == 0 {
		t.Fatal("partial pruning invalidated all required phrase pages")
	}
	for key := range l.want {
		mustOK(t, l.s.DeleteObject("b", key))
		delete(l.want, key)
	}
	finalize()
	l.verifyOK("phrase cohort reclamation")
	released := count()
	if released != 0 {
		t.Fatalf("last deletion retained %d unreachable phrase pages", released)
	}
	return retained, released
}
