package main

// FB-01 fuzz targets for the readers FB-01 changed: the journal scan with its
// evidence probe, and the checkpoint readers an open (and the off-mode
// witness) now consult. Run each for a bounded time, e.g.
//   go test -run '^$' -fuzz '^FuzzFB01JournalScan$' -fuzztime 30s .

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

// fb01Encode builds a well-formed v1 journal of n frames.
func fb01Encode(first uint64, n int) []byte {
	var out []byte
	for i := range n {
		p := []byte(fmt.Sprintf(`{"bucket":"b","key":"k%03d","size":%d}`, i, i*7))
		var h [journalHeaderSize]byte
		copy(h[0:4], journalMagic)
		binary.BigEndian.PutUint16(h[4:6], journalFrameVersion)
		h[6] = recordTypePutObjectRootV2
		binary.BigEndian.PutUint64(h[8:16], first+uint64(i))
		binary.BigEndian.PutUint32(h[16:20], uint32(len(p)))
		start := len(out)
		out = append(out, h[:]...)
		out = append(out, p...)
		out = binary.BigEndian.AppendUint32(out, crc32.Checksum(out[start:], castagnoliTable))
	}
	return out
}

func FuzzFB01JournalScan(f *testing.F) {
	good := fb01Encode(1, 6)
	f.Add(good, uint16(0), byte(0), uint16(0))
	f.Add(good, uint16(60), byte(1<<4), uint16(0))       // a length bit in frame 1
	f.Add(good, uint16(0), byte(0), uint16(len(good)-9)) // torn tail
	f.Add([]byte{}, uint16(0), byte(0), uint16(0))
	f.Add([]byte("ZSJ1ZSJ1ZSJ1"), uint16(3), byte(0xff), uint16(0))
	f.Fuzz(func(t *testing.T, data []byte, at uint16, xor byte, cut uint16) {
		if len(data) > 1<<20 {
			return
		}
		d := bytes.Clone(data)
		if len(d) > 0 {
			d[int(at)%len(d)] ^= xor
		}
		if cut > 0 && int(cut) < len(d) {
			d = d[:cut]
		}
		r := bytes.NewReader(d)
		sc, _, err := scanJournalRange(r, 0, int64(len(d)), 0, false, nil)
		if err != nil {
			t.Fatalf("scan of an in-memory image failed: %v", err)
		}
		if sc.ValidEnd < 0 || sc.ValidEnd > int64(len(d)) || (sc.Damage == nil) != (sc.ValidEnd == int64(len(d))) {
			t.Fatalf("inconsistent scan: %+v", sc)
		}
		again, _, _ := scanJournalRange(r, 0, int64(len(d)), 0, false, nil)
		if again.ValidEnd != sc.ValidEnd || again.LastSeq != sc.LastSeq || (again.Damage == nil) != (sc.Damage == nil) {
			t.Fatal("scan is not deterministic")
		}
		prefix, _, _ := scanJournalRange(r, 0, sc.ValidEnd, 0, false, nil)
		if prefix.Damage != nil || prefix.Frames != sc.Frames || prefix.LastSeq != sc.LastSeq {
			t.Fatalf("the valid prefix does not rescan cleanly: %+v", prefix)
		}
		if dm := sc.Damage; dm != nil {
			if dm.Offset != sc.ValidEnd || dm.Seq != sc.LastSeq+1 || dm.Cause == "" || dm.Detail == "" {
				t.Fatalf("damage does not describe the scan: %+v", dm)
			}
			var buf []byte
			if dm.LaterValid >= 0 {
				var h [journalHeaderSize]byte
				copy(h[:], d[dm.LaterValid:])
				seq, ok, _, _ := journalFrameAt(r, dm.LaterValid, int64(len(d)), int64(binary.BigEndian.Uint32(h[16:20])), &buf)
				if !ok || seq != dm.LaterSeq || dm.LaterValid <= dm.Offset {
					t.Fatalf("reported later frame does not check out: %+v", dm)
				}
			}
			if dm.CorrectLen >= 0 {
				if seq, ok, _, _ := journalFrameAt(r, dm.Offset, int64(len(d)), dm.CorrectLen, &buf); !ok || seq != dm.Seq {
					t.Fatalf("reported corrected length does not check out: %+v", dm)
				}
			}
			if journalPolicyStrict.tolerates(dm) {
				t.Fatal("the strict policy tolerated damage")
			}
		}
		if ve, _, n, _, lerr := replayJournalRange(r, 0, int64(len(d)), 0, false, nil); lerr == nil && (ve != sc.ValidEnd || n != sc.Frames) {
			t.Fatal("the legacy wrapper disagrees with the scan")
		}
	})
}

func FuzzFB01CheckpointReaders(f *testing.F) {
	dir := f.TempDir()
	fb01SeedCheckpoint(f, dir, 4, 1)
	if good, err := os.ReadFile(filepath.Join(dir, "journal", nsGenName)); err == nil {
		f.Add(good, false)
	}
	f.Add([]byte("ZS3NSC3\nshort"), true)
	f.Add([]byte{}, false)
	f.Fuzz(func(t *testing.T, data []byte, legacy bool) {
		if len(data) > 1<<20 {
			return
		}
		root := t.TempDir()
		os.MkdirAll(filepath.Join(root, "journal"), 0o755)
		if legacy {
			os.WriteFile(filepath.Join(root, "journal", namespaceCompactCheckpointName), data, 0o644)
			s := &Store{root: root}
			s.legacyCheckpointHeader(true)
			return
		}
		p := filepath.Join(root, "journal", nsGenName)
		os.WriteFile(p, data, 0o644)
		fh, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		g, verify, err := openNSGen(fh, nil)
		if err != nil {
			fh.Close()
			return
		}
		if verify() == nil {
			g.check()
		}
		g.release()
	})
}
