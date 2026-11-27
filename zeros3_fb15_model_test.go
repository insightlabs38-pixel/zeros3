package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"
)

type fb15ModelBody struct {
	chunks []int
	bases  []int
}

type fb15ModelEvent struct {
	seq  uint64
	root int
	old  int
	next int
}

// The checksum expectation comes from the independently retained commit
// witness, never from the proposed delta stream.
func fb15EventsSHA(events []fb15ModelEvent) [32]byte {
	h := sha256.New()
	var b [32]byte
	for _, e := range events {
		binary.BigEndian.PutUint64(b[0:8], e.seq)
		binary.BigEndian.PutUint64(b[8:16], uint64(e.root))
		binary.BigEndian.PutUint64(b[16:24], uint64(e.old))
		binary.BigEndian.PutUint64(b[24:32], uint64(e.next))
		h.Write(b[:])
	}
	return [32]byte(h.Sum(nil))
}

func fb15ApplyModel(roots []int, events []fb15ModelEvent, nextSeq uint64, witness [32]byte, count []uint32, closure [][]int) bool {
	if fb15EventsSHA(events) != witness {
		return false
	}
	touched := make(map[int]int)
	for _, e := range events {
		if e.seq != nextSeq || e.root < 0 || e.root >= len(roots) || e.next < -1 || e.next >= len(closure) {
			return false
		}
		nextSeq++
		v, ok := touched[e.root]
		if !ok {
			v = roots[e.root]
		}
		if e.old != v {
			return false
		}
		touched[e.root] = e.next
	}
	for root, next := range touched {
		if next >= 0 {
			for _, record := range closure[next] {
				count[record]++
			}
		}
		if prior := roots[root]; prior >= 0 {
			for _, record := range closure[prior] {
				count[record]--
			}
		}
		roots[root] = next
	}
	return true
}

func fb15CanonicalModel(roots []int, bodies []fb15ModelBody, allocated []bool) []int {
	visited := make([]bool, len(bodies))
	live := make([]bool, len(allocated))
	queue := slices.Clone(roots)
	for i := 0; i < len(queue); i++ {
		body := queue[i]
		if body < 0 || visited[body] {
			continue
		}
		visited[body] = true
		for _, record := range bodies[body].chunks {
			live[record] = true
		}
		queue = append(queue, bodies[body].bases...)
	}
	var deletions []int
	for record, have := range allocated {
		if have && !live[record] {
			deletions = append(deletions, record)
		}
	}
	return deletions
}

func TestFB15ExactCandidateModel(t *testing.T) {
	const rootsN, bodiesN, recordsN, phases = 96, 140, 192, 480
	for seed := uint64(1); seed <= 8; seed++ {
		t.Run(strconv.FormatUint(seed, 10), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 7329))
			bodies := make([]fb15ModelBody, bodiesN)
			closure := make([][]int, bodiesN)
			for id := range bodies {
				body := &bodies[id]
				for range 1 + rng.IntN(4) {
					body.chunks = append(body.chunks, rng.IntN(recordsN))
				}
				if id > 0 && rng.IntN(3) == 0 {
					body.bases = append(body.bases, rng.IntN(id))
				}
				if id > 1 && rng.IntN(8) == 0 {
					body.bases = append(body.bases, rng.IntN(id))
				}
				closure[id] = slices.Clone(body.chunks)
				for _, base := range body.bases {
					closure[id] = append(closure[id], closure[base]...)
				}
				slices.Sort(closure[id])
				closure[id] = slices.Compact(closure[id])
			}
			wantRoots := make([]int, rootsN)
			for id := range wantRoots {
				wantRoots[id] = rng.IntN(bodiesN+1) - 1
			}
			tracked := slices.Clone(wantRoots)
			counts := make([]uint32, recordsN)
			reseed := func() {
				clear(counts)
				copy(tracked, wantRoots)
				for _, id := range wantRoots {
					if id >= 0 {
						for _, record := range closure[id] {
							counts[record]++
						}
					}
				}
			}
			reseed()
			allocated := make([]bool, recordsN)
			var seq uint64
			var confirmed, refused, lostHints, rejectedCounter int
			for step := range phases {
				// Independent current/history/snapshot/multipart root slots
				// and aliases share the generated content DAG.
				events := make([]fb15ModelEvent, 0, 6)
				for i := 0; i < 2+rng.IntN(5); i++ {
					root := rng.IntN(rootsN)
					next := rng.IntN(bodiesN+1) - 1
					events = append(events, fb15ModelEvent{seq + uint64(i) + 1, root, wantRoots[root], next})
					wantRoots[root] = next
				}
				for i := range allocated {
					// Provide the current live closure plus occasional orphans.
					if rng.IntN(22) == 0 {
						allocated[i] = true
					}
				}
				for _, id := range wantRoots {
					if id >= 0 {
						for _, record := range closure[id] {
							allocated[record] = true
						}
					}
				}
				witness := fb15EventsSHA(events)
				proposed := slices.Clone(events)
				rebuilt := false
				switch step % 19 {
				case 2: // missing frame
					proposed = proposed[1:]
				case 5: // duplicate delivery
					proposed = append(proposed, proposed[0])
				case 8: // reorder
					proposed[0], proposed[1] = proposed[1], proposed[0]
				case 11: // changed root or digest
					proposed[0].next = (proposed[0].next + 1) % bodiesN
				case 14: // crash or lost accelerator state
					tracked = make([]int, rootsN)
					reseed()
					lostHints++
					rebuilt = true
				}
				ok := rebuilt || fb15ApplyModel(tracked, proposed, seq+1, witness, counts, closure)
				seq += uint64(len(events))
				if !ok {
					refused++
					reseed() // exact mark replaces evidence before scheduling
				}
				if !slices.Equal(tracked, wantRoots) {
					t.Fatal("accepted a divergent root state")
				}
				confirmed++
				exact := fb15CanonicalModel(wantRoots, bodies, allocated)
				if step%19 == 17 {
					// A count may fail independently of the checked log.
					// An exact mark must veto this false candidate.
					for id, n := range counts {
						if n == 0 || !allocated[id] {
							continue
						}
						counts[id] = 0
						if slices.Contains(exact, id) {
							t.Fatal("canonical mark accepted a false zero")
						}
						rejectedCounter++
						reseed()
						break
					}
				}
				var candidates []int
				for id, n := range counts {
					if n == 0 && allocated[id] {
						candidates = append(candidates, id)
					}
				}
				if !slices.Equal(candidates, exact) {
					t.Fatalf("candidate and canonical deletion sets differ at step %d", step)
				}
				for _, id := range exact { // only independently canonical ids can retire
					allocated[id] = false
				}
			}
			t.Logf("confirmed=%d malformed_suffixes_rebuilt=%d lost_hint_states=%d false_zeroes_vetoed=%d identical_deletion_sets=%d",
				confirmed, refused, lostHints, rejectedCounter, confirmed)
		})
	}
}

func TestFB15GeneratedMarkScale(t *testing.T) {
	n, err := strconv.Atoi(os.Getenv("ZEROS3_FB15_SCALE"))
	if n == 0 {
		t.Skip("set ZEROS3_FB15_SCALE to between 1M and 25M roots")
	}
	mustOK(t, err)
	if n < 1_000_000 || n > 25_000_000 {
		t.Fatal("scale is outside 1M–25M roots")
	}
	const groups, passes, edits = 1 << 16, 32, 256
	st := time.Now()
	roots := make([]uint32, n)
	live := make([]uint32, n+groups+passes*edits)
	exact := make([]uint32, len(live))
	markAll := func(out []uint32) {
		clear(out)
		for root, id := range roots {
			out[id]++
			out[n+(root&(groups-1))]++
		}
	}
	for i := range roots {
		roots[i] = uint32(i)
	}
	markAll(live)
	seedTime := time.Since(st)
	start := time.Now()
	random := rand.New(rand.NewPCG(43482, 19232))
	var confirming, changed, maxHeld int64
	held := make([]uint32, 0, 4*edits)
	var retired int64
	fullRefs := int64(n) * 2 * passes
	for epoch := range passes {
		for event := range edits {
			row := random.IntN(n)
			old := roots[row]
			next := uint32(n + groups + epoch*edits + event)
			roots[row] = next
			live[next]++
			live[old]--
			held = append(held, old)
			changed += 2
		}
		if (epoch+1)%4 != 0 {
			maxHeld = max(maxHeld, int64(len(held))*4096)
			continue
		}
		markAll(exact)
		if !slices.Equal(live, exact) {
			t.Fatalf("scale confirmation failed at epoch %d", epoch)
		}
		confirming += int64(n) * 2
		for _, id := range held {
			if exact[id] != 0 {
				t.Fatalf("live record %d was nominated for deletion", id)
			}
			retired++
		}
		held = held[:0]
	}
	var usage syscall.Rusage
	mustOK(t, syscall.Getrusage(syscall.RUSAGE_SELF, &usage))
	ratio := float64(confirming+changed) / float64(fullRefs)
	if ratio > .35 || maxHeld*20 > int64(n)*4096 {
		t.Fatal(fmt.Sprintf("scale gate missed, work %.6f held %d", ratio, maxHeld))
	}
	t.Logf("roots=%d distinct_slots=%d passes=%d edits=%d marked_refs=%d changed_refs=%d full_refs=%d work_ratio=%.8f retired=%d max_held=%d max_rss_kib=%d seed=%s measured=%s",
		n, len(live), passes, edits, confirming, changed, fullRefs, ratio, retired, maxHeld, usage.Maxrss, seedTime, time.Since(start))
}

func TestFB15CheckedJournalSuffix(t *testing.T) {
	for _, format := range []string{"v1", "j2"} {
		t.Run(format, func(t *testing.T) {
			if format == "j2" {
				t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
			}
			l := newAuthLab(t)
			s := l.s
			if format == "j2" {
				mustOK(t, s.ensureFeature(journalBinFeature))
				s.writeBinaryJournal = true
			}
			o, q := s.journalEnd()
			start := fb15Cursor{o, q}
			l.put("one", bytes.Repeat([]byte("first version"), 99))
			o, q = s.journalEnd()
			mid := fb15Cursor{o, q}
			l.put("two", bytes.Repeat([]byte("second version"), 120))
			o, q = s.journalEnd()
			end := fb15Cursor{o, q}
			image := make([]byte, end.offset)
			_, err := s.journal.f.ReadAt(image, 0)
			mustOK(t, err)
			keys, n, err := fb15ChangedKeys(bytes.NewReader(image), start, end)
			mustOK(t, err)
			if n != 2 || !keys[fb15Key{"b", "one"}] || !keys[fb15Key{"b", "two"}] {
				t.Fatal("checked suffix lost a root key")
			}
			makeImage := func(regions ...[]byte) []byte { return bytes.Join(regions, nil) }
			prefix, first, second := image[:start.offset], image[start.offset:mid.offset], image[mid.offset:end.offset]
			corrupt := bytes.Clone(image)
			corrupt[mid.offset-1] ^= 1
			bad := [][]byte{image[:end.offset-1], makeImage(prefix, second), makeImage(prefix, first, first, second),
				makeImage(prefix, second, first), corrupt}
			for i, b := range bad {
				if keys, _, err := fb15ChangedKeys(bytes.NewReader(b), start, end); err == nil || keys != nil {
					t.Fatalf("damage sample %d yielded usable hints", i)
				}
			}
			l.close()
			l.open()
			l.exact("cold reopen after losing planner state")
			recovered, _, err := fb15Seed(l.s, map[[32]byte]int64{})
			mustOK(t, err)
			rr, err := l.s.reachabilityScan(false, nil)
			mustOK(t, err)
			if !rr.OK() || len(recovered.count) != rr.tbl.len() {
				t.Fatal("reopened canonical roots differ from rebuilt hints")
			}
			t.Logf("from=%+v mid=%+v end=%+v suffix_sha256=%x refused=%d", start, mid, end, sha256.Sum256(image[start.offset:]), len(bad))
		})
	}
}
