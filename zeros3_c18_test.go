package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func c18PackFixture(t *testing.T) (string, string, []packEntry, map[string][]byte) {
	t.Helper()
	dictionary := genRandomBytes(18171, 8192)
	bodies := map[string][]byte{}
	for i := range 32 {
		pos := i * 177
		bodies[fmt.Sprintf("fragment-%02d", i)] = bytes.Clone(dictionary[pos : pos+2048])
	}
	dir, _ := bundleFixture(t, bodies)
	_, err := repackStore(dir, repackOptions{TargetBytes: 1 << 20, MaxLivePercent: 100,
		Compress: true, Recompress: true, Prefix: dictionary})
	mustOK(t, err)
	s, err := OpenStoreShared(dir)
	mustOK(t, err)
	defer s.Close()
	for _, info := range s.packSnap().packs {
		if info.presetDict {
			_, entries, err := loadPackFile(info.path)
			mustOK(t, err)
			return dir, info.path, entries, bodies
		}
	}
	t.Fatal("fixture did not publish a pack with redundant preset copies")
	return "", "", nil, nil
}

func TestC18PackDictionaryRepair(t *testing.T) {
	for _, fault := range []string{"primary", "backup", "both", "unrelated-member"} {
		t.Run(fault, func(t *testing.T) {
			dir, packPath, records, bodies := c18PackFixture(t)
			original, err := os.ReadFile(packPath)
			mustOK(t, err)
			if fault != "backup" {
				b5Flip(t, packPath, int64(records[0].off)+101)
			}
			if fault == "backup" || fault == "both" {
				b5Flip(t, packPath, int64(records[len(records)-1].off)+101)
			}
			if fault == "unrelated-member" {
				b5Flip(t, packPath, int64(records[1].off)+1)
			}
			failed, err := os.ReadFile(packPath)
			mustOK(t, err)
			lock, err := acquireStoreLock(dir, true)
			mustOK(t, err)
			defer lock.release()
			s, err := OpenStoreShared(dir)
			mustOK(t, err)
			defer s.Close()
			degraded, err := s.Verify(true)
			mustOK(t, err)
			if degraded.OK() {
				t.Fatal("deep verify did not detect a damaged hidden copy")
			}
			changed, err := s.repairPackDictionaries()
			after, readErr := os.ReadFile(packPath)
			mustOK(t, readErr)
			if fault == "both" || fault == "unrelated-member" {
				if err == nil || len(changed) != 0 || !bytes.Equal(after, failed) {
					t.Fatalf("refused repair altered the original: %+v %v", changed, err)
				}
				return
			}
			mustOK(t, err)
			if len(changed) != 1 || !bytes.Equal(after, original) || changed[0].RewrittenBytes != int64(len(original)) {
				t.Fatalf("repair did not restore physical identity: %+v", changed)
			}
			again, err := s.repairPackDictionaries()
			mustOK(t, err)
			if len(again) != 0 {
				t.Fatal("healthy pack needed another repair")
			}
			for key, want := range bodies {
				_, got, err := s.GetObject("b", key)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("repaired object %s differs: %v", key, err)
				}
			}
		})
	}
}

func TestC18PackDictionaryRepairCuts(t *testing.T) {
	if cut := os.Getenv("ZEROS3_C18_DICT_KILL"); cut != "" {
		dir := os.Getenv("ZEROS3_C18_DICT_STORE")
		lock, err := acquireStoreLock(dir, true)
		mustOK(t, err)
		defer lock.release()
		s, err := OpenStoreShared(dir)
		mustOK(t, err)
		defer s.Close()
		testHook = func(point string) {
			if point == cut {
				mustOK(t, syscall.Kill(os.Getpid(), syscall.SIGKILL))
			}
		}
		_, err = s.repairPackDictionaries()
		t.Fatalf("repair child failed to reach %s: %v", cut, err)
	}
	for _, cut := range []string{hookPackDictionaryBeforeRename, hookPackDictionaryAfterRename} {
		t.Run(cut, func(t *testing.T) {
			dir, path, entries, _ := c18PackFixture(t)
			b5Flip(t, path, int64(entries[0].off)+113)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestC18PackDictionaryRepairCuts$")
			cmd.Env = append(os.Environ(), "ZEROS3_C18_DICT_KILL="+cut, "ZEROS3_C18_DICT_STORE="+dir)
			msg, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if ctx.Err() != nil || !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("expected actual process cut: %v %s", err, msg)
			}
			if _, _, err := verifyPackFile(path); (err != nil) != (cut == hookPackDictionaryBeforeRename) {
				t.Fatalf("wrong pack visibility after %s: %v", cut, err)
			}
			lock, err := acquireStoreLock(dir, true)
			mustOK(t, err)
			defer lock.release()
			s, err := OpenStoreShared(dir)
			mustOK(t, err)
			defer s.Close()
			got, err := s.repairPackDictionaries()
			mustOK(t, err)
			want := 0
			if cut == hookPackDictionaryBeforeRename {
				want = 1
			}
			if len(got) != want {
				t.Fatalf("retry repairs %d, expected %d", len(got), want)
			}
			res, err := s.Verify(true)
			if err != nil || !res.OK() {
				t.Fatalf("reopened repair did not deep verify: %v %+v", err, res.Issues)
			}
		})
	}
}
