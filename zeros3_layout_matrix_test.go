package main

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// Behavioural requirements stated once and qualified on every storage layout
// (loose files, and the production default: segments / packs). Together with
// the layout-agnostic fault helpers these replace the coverage the old global
// "restore the loose threshold" override hid. Object sizes cover a small
// single-chunk object, a small multi-chunk object and a medium (direct pack)
// object.

var lmSizes = []int{8 << 10, 120 << 10, 700 << 10}

func lmStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, dir := p1cOpen(t)
	return s, dir
}

func lmPut(t *testing.T, s *Store, key string, seed, size int) ([]byte, *objectEntry) {
	t.Helper()
	body := genRandomBytes(int64(seed), size)
	e, err := s.PutObject("b", key, body, "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	return body, e
}

func lmChunks(t *testing.T, s *Store, e *objectEntry) []chunkRef {
	t.Helper()
	m, err := s.readVerifiedManifest(e.manifestUUID, e.manifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	return m.Chunks
}

func TestLayoutMatrix_VerifyDetectsMissingAndCorruptChunks(t *testing.T) {
	forEachLayout(t, func(t *testing.T) {
		for _, size := range lmSizes {
			t.Run(fmt.Sprint(size), func(t *testing.T) {
				s, _ := lmStore(t)
				_, e1 := lmPut(t, s, "gone", 1, size)
				_, e2 := lmPut(t, s, "rot", 2, size)
				c1 := lmChunks(t, s, e1)[0]
				c2 := lmChunks(t, s, e2)[0]
				p1cVerify(t, s)
				removeChunkAnyLayout(t, s, chunkSumHex(t, c1.SHA256))
				res, err := s.Verify(false)
				if err != nil || res.OK() || res.Missing == 0 {
					t.Fatalf("missing chunk not detected: %v %+v", err, res)
				}
				corruptChunkAnyLayout(t, s, chunkSumHex(t, c2.SHA256))
				deep, err := s.Verify(true)
				if err != nil || deep.OK() || deep.Corrupt == 0 {
					t.Fatalf("corrupt chunk not detected by deep verify: %v %+v", err, deep)
				}
				if _, _, err := s.GetObject("b", "rot"); err == nil {
					t.Fatal("a corrupt chunk was served")
				}
			})
		}
	})
}

func TestLayoutMatrix_RepairFindingsClassifyMissingAndCorrupt(t *testing.T) {
	forEachLayout(t, func(t *testing.T) {
		for _, size := range lmSizes {
			t.Run(fmt.Sprint(size), func(t *testing.T) {
				s, _ := lmStore(t)
				_, e1 := lmPut(t, s, "m", 3, size)
				_, e2 := lmPut(t, s, "c", 4, size)
				m := lmChunks(t, s, e1)[0]
				c := lmChunks(t, s, e2)[0]
				removeChunkAnyLayout(t, s, chunkSumHex(t, m.SHA256))
				corruptChunkAnyLayout(t, s, chunkSumHex(t, c.SHA256))
				fs, err := s.repairFindings()
				if err != nil {
					t.Fatal(err)
				}
				kinds := map[string]string{}
				for _, f := range fs {
					kinds[f.SHA256] = f.Kind
				}
				if kinds[m.SHA256] != "missing" || kinds[c.SHA256] != "corrupt" || len(fs) != 2 {
					t.Fatalf("findings = %+v", fs)
				}
			})
		}
	})
}

func TestLayoutMatrix_RestartDurabilityAndUncleanReopen(t *testing.T) {
	forEachLayout(t, func(t *testing.T) {
		s, dir := lmStore(t)
		want := map[string][]byte{}
		for i, size := range lmSizes {
			k := fmt.Sprintf("k%d", i)
			want[k], _ = lmPut(t, s, k, 10+i, size)
		}
		// unclean: the first store is abandoned without Close
		s2 := p1cReopen(t, dir)
		p1cCheck(t, s2, want)
		p1cVerify(t, s2)
		more, _ := lmPut(t, s2, "more", 20, 33<<10)
		want["more"] = more
		s2.Close()
		s3 := p1cReopen(t, dir)
		p1cCheck(t, s3, want)
		p1cVerify(t, s3)
	})
}

func TestLayoutMatrix_HistoryRestoreAndDeleteSurviveMaintenance(t *testing.T) {
	forEachLayout(t, func(t *testing.T) {
		for _, size := range lmSizes {
			t.Run(fmt.Sprint(size), func(t *testing.T) {
				s, dir := lmStore(t)
				v1, _ := lmPut(t, s, "k", 31, size)
				v2, _ := lmPut(t, s, "k", 32, size)
				gone, _ := lmPut(t, s, "gone", 33, size)
				_ = gone
				if err := s.DeleteObject("b", "gone"); err != nil {
					t.Fatal(err)
				}
				s.Close()
				res, err := gcCollect(dir, true)
				if err != nil || !res.LiveSetOK {
					t.Fatalf("gc: %v %+v", err, res)
				}
				s2 := p1cReopen(t, dir)
				p1cCheck(t, s2, map[string][]byte{"k": v2})
				hist := historyFor(t, s2, "b", "k")
				if len(hist) != 1 {
					t.Fatalf("history = %d versions", len(hist))
				}
				if _, _, err := s2.RestoreObjectVersion("b", "k", hist[0].versionID); err != nil {
					t.Fatalf("restore: %v", err)
				}
				p1cCheck(t, s2, map[string][]byte{"k": v1})
				if _, _, err := s2.GetObject("b", "gone"); !errors.Is(err, errNoSuchKey) {
					t.Fatalf("deleted object: %v", err)
				}
				p1cVerify(t, s2)
			})
		}
	})
}

func TestLayoutMatrix_GCReclaimsUnreferencedDataKeepsLiveData(t *testing.T) {
	forEachLayout(t, func(t *testing.T) {
		for _, size := range lmSizes {
			t.Run(fmt.Sprint(size), func(t *testing.T) {
				s, dir := lmStore(t)
				live := map[string][]byte{}
				live["live"], _ = lmPut(t, s, "live", 41, size)
				// a failed conditional PUT leaves unreferenced chunks (and a manifest)
				for i := 0; i < 3; i++ {
					if _, err := s.PutObjectChecked("b", "live", genRandomBytes(int64(100+i), size), "", nil, putCondition{ifNoneMatchStar: true}); err == nil {
						t.Fatal("conditional PUT succeeded")
					}
				}
				s.Close()
				dry, err := gcCollect(dir, false)
				if err != nil || !dry.LiveSetOK {
					t.Fatalf("gc dry-run: %v %+v", err, dry)
				}
				if dry.ReclaimablePayloadBytes == 0 && dry.PackSummary.PackedDeadBytes == 0 {
					t.Fatalf("dead data is not reported: %+v", dry)
				}
				if _, err := gcCollect(dir, true); err != nil {
					t.Fatal(err)
				}
				if _, err := repackStore(dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100}); err != nil {
					t.Fatal(err)
				}
				s2 := p1cReopen(t, dir)
				p1cCheck(t, s2, live)
				p1cVerify(t, s2)
			})
		}
	})
}

func TestLayoutMatrix_ConcurrentPutGetSameAndDifferentKeys(t *testing.T) {
	forEachLayout(t, func(t *testing.T) {
		s, dir := lmStore(t)
		const writers, per = 8, 12
		var mu sync.Mutex
		want := map[string][]byte{}
		var wg sync.WaitGroup
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < per; i++ {
					size := lmSizes[(w+i)%len(lmSizes)]
					body := genRandomBytes(int64(w*1000+i), size)
					k := fmt.Sprintf("u/%d/%d", w, i)
					if _, err := s.PutObject("b", k, body, "", nil); err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					want[k] = body
					mu.Unlock()
					if _, got, err := s.GetObject("b", k); err != nil || !bytes.Equal(got, body) {
						t.Errorf("read-your-write %s: %v", k, err)
					}
					// all writers also hit a few shared keys: the last committed version wins, never a blend
					sk := fmt.Sprintf("s/%d", i%3)
					sb := genRandomBytes(int64(500000+w*100+i), 9<<10)
					if _, err := s.PutObject("b", sk, sb, "", nil); err != nil {
						t.Error(err)
					}
				}
			}()
		}
		withTimeout(t, 120_000_000_000, "concurrent matrix", wg.Wait)
		p1cCheck(t, s, want)
		for i := 0; i < 3; i++ {
			_, got, err := s.GetObject("b", fmt.Sprintf("s/%d", i))
			if err != nil || len(got) != 9<<10 {
				t.Fatalf("shared key s/%d: %v len=%d", i, err, len(got))
			}
		}
		p1cVerify(t, s)
		s.Close()
		s2 := p1cReopen(t, dir)
		p1cCheck(t, s2, want)
		p1cVerify(t, s2)
	})
}
