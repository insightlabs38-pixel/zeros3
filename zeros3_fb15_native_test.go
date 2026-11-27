package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type fb15Key struct{ bucket, key string }
type fb15Cursor struct {
	offset   int64
	sequence uint64
}

var fb15FullMark = errors.New("incremental maintenance requires a fresh canonical mark")

// fb15ChangedKeys derives the refresh set from actual checked journal bytes.
// Unknown kinds fall back when their root scope cannot be proven here.
func fb15ChangedKeys(f io.ReaderAt, from, to fb15Cursor) (map[fb15Key]bool, int, error) {
	if from == to {
		return nil, 0, nil
	}
	if to.offset <= from.offset || to.sequence <= from.sequence {
		return nil, 0, fb15FullMark
	}
	keys := map[fb15Key]bool{}
	end, seq, n, applyErr, err := replayJournalRange(f, from.offset, to.offset, from.sequence, false, func(r journalRecord) error {
		switch r.recType {
		case recordTypePutObjectRootV2:
			if bytes.HasPrefix(r.payload, []byte(journalBinPut)) {
				var p journalPutPayloadV2
				if _, err := decodeJournalBinPut(r.payload, &p); err != nil {
					return err
				}
				keys[fb15Key{p.Bucket, p.Key}] = true
				return nil
			}
			fallthrough
		case recordTypePutObjectRoot, recordTypeDeleteObjectRoot, recordTypeDeleteObjectRootV2:
			var p struct{ Bucket, Key string }
			if err := json.Unmarshal(r.payload, &p); err != nil {
				return err
			}
			keys[fb15Key{p.Bucket, p.Key}] = true
		case recordTypePruneHistory:
			p, err := parsePruneHistoryPayload(r.payload)
			if err != nil {
				return err
			}
			for _, key := range p.Entries {
				keys[fb15Key{key.Bucket, key.Key}] = true
			}
		case recordTypeCreateBucket:
			return nil
		default:
			return fb15FullMark
		}
		return nil
	})
	if err != nil || applyErr != nil || end != to.offset || seq != to.sequence || uint64(n) != to.sequence-from.sequence {
		return nil, 0, errors.Join(fb15FullMark, err, applyErr)
	}
	return keys, n, nil
}

type fb15ManifestID struct {
	id  string
	sum [32]byte
}

type fb15Counter struct {
	rows  map[fb15Key]map[fb15ManifestID][][32]byte
	count map[[32]byte]uint32
	// pending is a hint for accounting and scheduling. Canonical mark
	// remains the source of authority for the independent deleter.
	pending   map[[32]byte]int64
	liveBytes int64
	heldBytes int64
}

func newFB15Counter() *fb15Counter {
	return &fb15Counter{
		rows:  make(map[fb15Key]map[fb15ManifestID][][32]byte),
		count: make(map[[32]byte]uint32), pending: make(map[[32]byte]int64),
	}
}

func (c *fb15Counter) replace(key fb15Key, next map[fb15ManifestID][][32]byte, extents map[[32]byte]int64) int64 {
	prior := c.rows[key]
	var touched int64
	// Add first so replacing references to the same digest incurs no
	// artificial retirement edge. Manifests themselves are immutable.
	for id, refs := range next {
		if _, ok := prior[id]; ok {
			continue
		}
		for _, sum := range refs {
			touched++
			if c.count[sum] == 0 {
				bytes := extents[sum]
				c.liveBytes += bytes
				c.heldBytes -= c.pending[sum]
				delete(c.pending, sum)
			}
			c.count[sum]++
		}
	}
	for id, refs := range prior {
		if _, ok := next[id]; ok {
			continue
		}
		for _, sum := range refs {
			touched++
			n := c.count[sum]
			if n == 0 {
				panic("incremental test model lost a reference")
			}
			if n > 1 {
				c.count[sum]--
				continue
			}
			delete(c.count, sum)
			bytes := extents[sum]
			c.pending[sum] = bytes
			c.heldBytes += bytes
			c.liveBytes -= bytes
		}
	}
	if len(next) == 0 {
		delete(c.rows, key)
	} else {
		c.rows[key] = next
	}
	return touched
}

func fb15LoadRefs(s *Store, id fb15ManifestID, sizes map[[32]byte]int64) ([][32]byte, error) {
	man, err := s.readVerifiedManifest(id.id, id.sum)
	if err != nil {
		return nil, err
	}
	refs := make([][32]byte, 0, len(man.Chunks))
	for _, c := range man.Chunks {
		sum, err := decodeHexSHA256(c.SHA256)
		if err != nil {
			return nil, err
		}
		if _, ok := sizes[sum]; !ok {
			// Price the current live record. The sample stores use the
			// native locator when packed and file bytes otherwise.
			if loc, ok := s.packLookup(sum); ok {
				sizes[sum] = int64(loc.stored) + packRecordBytes
			} else {
				sizes[sum] = c.Length
			}
		}
		refs = append(refs, sum)
	}
	return refs, nil
}

func fb15Refresh(s *Store, key fb15Key, current map[fb15ManifestID][][32]byte, sizes map[[32]byte]int64) (map[fb15ManifestID][][32]byte, error) {
	live := make(map[fb15ManifestID][][32]byte)
	keep := func(id fb15ManifestID) error {
		r, ok := current[id]
		var err error
		if !ok {
			r, err = fb15LoadRefs(s, id, sizes)
		}
		if err == nil {
			live[id] = r
		}
		return err
	}
	history, entry, err := s.ListVersions(key.bucket, key.key)
	if err != nil {
		return nil, err
	}
	if entry != nil {
		if err := keep(fb15ManifestID{entry.manifestUUID, entry.manifestSHA256}); err != nil {
			return nil, err
		}
	}
	for _, h := range history {
		if err := keep(fb15ManifestID{h.manifestUUID, h.manifestSHA256}); err != nil {
			return nil, err
		}
	}
	return live, nil
}

func fb15Seed(s *Store, sizes map[[32]byte]int64) (*fb15Counter, int64, error) {
	c := newFB15Counter()
	roots := make(map[fb15Key]map[fb15ManifestID][][32]byte)
	err := s.nsEachRoot(nil, func(_ bool, key, uuid []byte, sum [32]byte) error {
		b, k, _ := bytes.Cut(key, []byte{0})
		root := fb15Key{string(b), string(k)}
		if roots[root] == nil {
			roots[root] = make(map[fb15ManifestID][][32]byte)
		}
		id := fb15ManifestID{string(uuid), sum}
		if _, ok := roots[root][id]; ok {
			return nil
		}
		r, err := fb15LoadRefs(s, id, sizes)
		if err == nil {
			roots[root][id] = r
		}
		return err
	})
	var work int64
	for key, refs := range roots {
		work += c.replace(key, refs, sizes)
	}
	return c, work, err
}

type fb15File struct {
	raw []byte
	sha [32]byte
}

func fb15Tar(t *testing.T, path string) map[string]fb15File {
	t.Helper()
	f, err := os.Open(path)
	mustOK(t, err)
	defer f.Close()
	r := tar.NewReader(f)
	rows := make(map[string]fb15File)
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		mustOK(t, err)
		if h.Typeflag != tar.TypeReg {
			continue
		}
		raw, err := io.ReadAll(r)
		mustOK(t, err)
		name := strings.TrimPrefix(h.Name, "./")
		rows[name] = fb15File{raw, sha256.Sum256(raw)}
	}
	return rows
}

func fb15MeasureTrace(t *testing.T, l *authLab, steps int, update func(*Store, int)) (float64, float64) {
	t.Helper()
	l.reclaim()
	s := l.s
	s.writeBinaryManifests = true
	sizes := map[[32]byte]int64{}
	tracker, seedRefs, err := fb15Seed(s, sizes)
	mustOK(t, err)
	o, q := s.journalEnd()
	cursor := fb15Cursor{o, q}
	var fullWork, changeWork, confirmWork int64
	var maxHold float64
	var passes, canonical, fallbacks int
	var incrementalTime, fullTime time.Duration
	retired := map[[32]byte]bool{}
	lastConfirmation := 0
	for i := range steps {
		update(s, i)

		start := time.Now()
		o, q := s.journalEnd()
		nextCursor := fb15Cursor{o, q}
		keys, frames, err := fb15ChangedKeys(s.journal.f, cursor, nextCursor)
		mustOK(t, err)
		var refs int64
		for key := range keys {
			after, err := fb15Refresh(s, key, tracker.rows[key], sizes)
			mustOK(t, err)
			refs += tracker.replace(key, after, sizes)
		}
		incrementalTime += time.Since(start)
		start = time.Now()
		rr, err := s.reachabilityScan(false, nil)
		mustOK(t, err)
		if !rr.OK() {
			t.Fatalf("canonical mark refused: %+v", rr.Issues)
		}
		fullTime += time.Since(start)
		var allRefs int64
		exact, work, err := fb15Seed(s, sizes)
		mustOK(t, err)
		allRefs = work
		if !maps.Equal(tracker.count, exact.count) || len(tracker.count) != rr.tbl.len() {
			t.Fatalf("incremental counts differ from exact roots in revision %d", i+1)
		}
		for sum := range tracker.count {
			if !rr.tbl.has(sum) {
				t.Fatalf("incremental digest %x is not in the authoritative mark", sum)
			}
		}
		for sum := range retired {
			if rr.tbl.has(sum) {
				delete(retired, sum)
			}
		}
		confirm := passes+1-lastConfirmation >= 4 || tracker.heldBytes*20 > tracker.liveBytes
		if confirm {
			canonical++
			lastConfirmation = passes + 1
			confirmWork += allRefs
			exactDeletes := map[[32]byte]int64{}
			for id, size := range sizes {
				if !rr.tbl.has(id) && !retired[id] {
					exactDeletes[id] = size
				}
			}
			if !maps.Equal(exactDeletes, tracker.pending) {
				fallbacks++
				t.Fatalf("scheduled candidate set differs from the canonical deletion set")
			}
			for id := range exactDeletes {
				retired[id] = true
			}

			clear(tracker.pending)
			tracker.heldBytes = 0
		}
		passes++
		hold := float64(tracker.heldBytes) / float64(max(1, tracker.liveBytes))
		maxHold = max(maxHold, hold)
		changeWork += refs
		fullWork += allRefs
		cursor = nextCursor
		t.Logf("pass=%d frames=%d changed_keys=%d delta_refs=%d full_refs=%d held_bytes=%d live_bytes=%d held_ratio=%.6f canonical=%t",
			passes, frames, len(keys), refs, allRefs, tracker.heldBytes, tracker.liveBytes, hold, confirm)
	}
	t.Logf("passes=%d seed_refs=%d full_refs=%d confirming_refs=%d delta_refs=%d canonical=%d fallbacks=%d mark_work_ratio=%.6f max_held_ratio=%.6f incremental_time=%s diagnostic_full_mark_time=%s",
		passes, seedRefs, fullWork, confirmWork, changeWork, canonical, fallbacks, float64(confirmWork+changeWork)/float64(fullWork), maxHold, incrementalTime, fullTime)
	return float64(confirmWork+changeWork) / float64(fullWork), maxHold
}

func TestFB15NativeJournalTrace(t *testing.T) {
	root := os.Getenv("ZEROS3_FB15_X9")
	if root == "" {
		t.Skip("set ZEROS3_FB15_X9 to the X9 tree-series directory")
	}
	paths, err := filepath.Glob(filepath.Join(root, "*.tar"))
	mustOK(t, err)
	if len(paths) != 8 {
		t.Fatal("the replay expects all eight X9 tree versions")
	}
	l := newAuthLab(t)
	last := fb15Tar(t, paths[0])
	fb15PutTree(t, l.s, nil, last, false)
	fb15MeasureTrace(t, l, len(paths)-1, func(s *Store, i int) {
		next := fb15Tar(t, paths[i+1])
		fb15PutTree(t, s, last, next, false)
		last = next
		keep := 1
		_, err := pruneHistoryStoreOpen(s, historyPruneOptions{Bucket: "b", KeepLast: &keep})
		mustOK(t, err)
	})
}

func fb15PutTree(t *testing.T, s *Store, last, next map[string]fb15File, backup bool) {
	t.Helper()
	for _, name := range slices.Sorted(maps.Keys(next)) {
		body := next[name]
		before, exists := last[name]
		if exists && before.sha == body.sha && !backup {
			continue
		}
		_, err := s.PutObject("b", "tree/"+name, body.raw, "application/octet-stream", nil)
		mustOK(t, err)
	}
	for _, name := range slices.Sorted(maps.Keys(last)) {
		if _, exists := next[name]; !exists {
			mustOK(t, s.DeleteObject("b", "tree/"+name))
		}
	}
}

func TestFB15ReconstructedX10Trace(t *testing.T) {
	root, tools := os.Getenv("ZEROS3_FB15_X10"), os.Getenv("ZEROS3_FB15_TOOLS")
	if root == "" || tools == "" {
		t.Skip("set ZEROS3_FB15_X10 to the F6 corpus and ZEROS3_FB15_TOOLS to regenerated toolchain archives")
	}
	source, err := filepath.Glob(filepath.Join(root, "src-series", "*.go"))
	mustOK(t, err)
	trees, err := filepath.Glob(filepath.Join(root, "tree-series", "*.tar"))
	mustOK(t, err)
	if len(source) != 32 || len(trees) != 8 {
		t.Fatalf("X10 needs 32 source revisions and eight tree backups, got %d and %d", len(source), len(trees))
	}
	put := func(s *Store, key, path string) {
		raw, err := os.ReadFile(path)
		mustOK(t, err)
		t.Logf("input=%s key=%s bytes=%d sha256=%x", filepath.Base(path), key, len(raw), sha256.Sum256(raw))
		_, err = s.PutObject("b", key, raw, "application/octet-stream", nil)
		mustOK(t, err)
	}
	l := newAuthLab(t)
	l.s.writeBinaryManifests = true
	put(l.s, "source", source[0])
	put(l.s, "gosrc", filepath.Join(tools, "gosrc.tar"))
	var previous map[string]fb15File
	ratio, held := fb15MeasureTrace(t, l, len(source)-1, func(s *Store, i int) {
		step := i + 2
		put(s, "source", source[step-1])
		if step%4 == 0 {
			next := fb15Tar(t, trees[step/4-1])
			fb15PutTree(t, s, previous, next, true)
			previous = next
		}
		if step == 17 {
			put(s, "gotools", filepath.Join(tools, "gotools.tar"))
		}
		keep := 3
		_, err := pruneHistoryStoreOpen(s, historyPruneOptions{Bucket: "b", KeepLast: &keep})
		mustOK(t, err)
	})
	if held > .05 || ratio > .35 {
		t.Fatalf("real X10 gates missed, mark %.4f held %.4f", ratio, held)
	}
}
