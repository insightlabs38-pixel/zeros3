package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFB10JournalCursorAndBenignX14(t *testing.T) {
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprint(compact), func(t *testing.T) {
			dir := t.TempDir()
			s, err := openStoreWithFeatures(dir, true, true, false, false, false, storeResearch{binary: compact})
			mustOK(t, err)
			mustOK(t, s.CreateBucket("b"))
			_, err = s.PutObject("b", "key", []byte("acknowledged"), "text/plain", nil)
			mustOK(t, err)
			before, err := readJournalCursor(dir, nil)
			mustOK(t, err)
			mustOK(t, s.nsConsolidate(true))
			_, err = s.PutObject("b", "key", []byte("overwrite"), "text/plain", nil)
			mustOK(t, err)
			after, err := readJournalCursor(dir, &before)
			mustOK(t, err)
			if after.Offset <= before.Offset || after.Group != before.Group+1 || after.Sequence != before.Sequence+1 ||
				after.Epoch != before.Epoch || after.Digest == before.Digest {
				t.Fatalf("unexpected lineage movement: %+v -> %+v", before, after)
			}
			path := filepath.Join(dir, "journal", "visibility.log")
			mustOK(t, s.Close())
			full, err := os.ReadFile(path)
			mustOK(t, err)
			foreign := before
			foreign.Epoch = strings.Repeat("0", 64)
			if _, err := readJournalCursor(dir, &foreign); err == nil {
				t.Fatal("accepted foreign epoch")
			}
			forged := before
			forged.Offset++
			if _, err := readJournalCursor(dir, &forged); err == nil {
				t.Fatal("accepted in-group cursor")
			}
			forged = before
			forged.Digest = strings.Repeat("0", 64)
			if _, err := readJournalCursor(dir, &forged); err == nil {
				t.Fatal("accepted wrong prefix digest")
			}
			width := int(after.Offset - before.Offset)
			cuts := []int{0, 1, journalGroupBegin - 1, journalGroupBegin,
				width / 4, width / 3, width / 2, 2 * width / 3,
				width - journalGroupFooter, width - journalGroupFooter/2, width - 1}
			for _, stale := range []bool{false, true} {
				for _, cut := range cuts {
					broken := bytes.Clone(full[:int(before.Offset)+cut])
					if stale {
						broken = append(broken, bytes.Repeat([]byte{0xa5}, width-cut)...)
					}
					mustOK(t, os.WriteFile(path, broken, 0600))
					v, err := OpenStore(dir)
					mustOK(t, err)
					_, body, err := v.GetObject("b", "key")
					mustOK(t, err)
					hist, err := v.histFind("b", "key")
					mustOK(t, err)
					if string(body) != "acknowledged" || len(hist) != 0 {
						t.Fatalf("at cut=%d stale=%t lost acknowledged state or retained partial overwrite", cut, stale)
					}
					if cut > 0 || stale {
						if v.journal.recovered == nil {
							t.Fatalf("at cut=%d stale=%t lost recovery evidence", cut, stale)
						}
					}
					mustOK(t, v.Close())
					live, err := os.ReadFile(path)
					mustOK(t, err)
					if !bytes.Equal(live, full[:before.Offset]) {
						t.Fatalf("at cut=%d stale=%t did not preserve exact acknowledged journal prefix", cut, stale)
					}
				}
			}
			if _, err := readJournalCursor(dir, &after); err == nil || !strings.Contains(err.Error(), "ahead") {
				t.Fatalf("external cursor did not detect exact rollback: %v", err)
			}
			if restored, err := readJournalCursor(dir, &before); err != nil || restored != before {
				t.Fatalf("exact earlier cursor no longer matches its prefix: %+v %v", restored, err)
			}
			t.Logf("compact=%t S1=%d S2=%d acknowledged loss=0 benign refusals=0; independent later cursor detected coherent prefix rollback", compact, len(cuts), len(cuts))
		})
	}
}

func TestFB10JournalSignedMixed(t *testing.T) {
	input := os.Getenv("FB10_SIGNED_ARCHIVE")
	if input == "" {
		t.Skip("set FB10_SIGNED_ARCHIVE to the pinned C19 urllib3 archive")
	}
	archive, err := os.ReadFile(input)
	mustOK(t, err)
	// The matching FB-J2 fixture increases the writer count fourfold in
	// its focused profile setting. Use it here for a denser tail sample.
	t.Setenv("FBJ2_PROFILE_MODES", "fb10")
	modes := []string{"lazy", "j2", "j2bin"}
	for round := range 6 {
		for i := range modes {
			fbj2SignedRun(t, modes[(i+round)%len(modes)], round, 32, 32<<10, true, archive)
		}
	}
}
