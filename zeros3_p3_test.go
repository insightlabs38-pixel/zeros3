package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"maps"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A shared (read-only) opener must never cut the journal: a live writer may
// append a frame between the opener's replay and the point where a torn tail
// would be truncated.
func TestP3_SharedOpenerNeverTruncatesALiveJournal(t *testing.T) {
	s, dir := p1cOpen(t)
	p1cPutN(t, s, "a", 3, 2000)
	late := p1cBody(99, 3000)
	fired := false
	testHook = func(point string) {
		if point == hookJournalReplayed && !fired {
			fired = true
			if _, err := s.PutObject("b", "late", late, "application/octet-stream", nil); err != nil {
				t.Error(err)
			}
		}
	}
	defer func() { testHook = nil }()
	ro, err := OpenStoreShared(dir)
	testHook = nil
	if err != nil {
		t.Fatal(err)
	}
	ro.Close()
	if !fired {
		t.Fatal("hook did not fire")
	}
	// The live writer goes on appending, then the process restarts.
	if _, err := s.PutObject("b", "after", p1cBody(100, 1000), "application/octet-stream", nil); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen after a shared open raced with a writer: %v", err)
	}
	defer s2.Close()
	for _, k := range []string{"late", "after"} {
		if _, got, err := s2.GetObject("b", k); err != nil || len(got) == 0 {
			t.Fatalf("acknowledged object %q lost: %v", k, err)
		} else if k == "late" && !bytes.Equal(got, late) {
			t.Fatal("late object differs")
		}
	}
}

// openJournalCollect is the owner-mode openJournal returning the records, for
// tests that inspect them.
func openJournalCollect(path string) (*Journal, []journalRecord, error) {
	var recs []journalRecord
	j, applyErr, err := openJournal(path, false, func(r journalRecord) error {
		r.payload = bytes.Clone(r.payload)
		recs = append(recs, r)
		return nil
	})
	if err == nil {
		err = applyErr
	}
	return j, recs, err
}

// journalReference is the original frame-at-a-time replay, kept as the oracle
// for the streaming implementation.
func journalReference(f *os.File) (validEnd int64, lastSeq uint64, records []journalRecord, err error) {
	var offset int64
	for {
		header := make([]byte, journalHeaderSize)
		n, rerr := f.ReadAt(header, offset)
		if rerr == io.EOF {
			_ = n
			return offset, lastSeq, records, nil
		}
		if rerr != nil {
			return 0, 0, nil, fmt.Errorf("journal: read failed at offset %d: %w", offset, rerr)
		}
		if string(header[0:4]) != journalMagic {
			return 0, 0, nil, fmt.Errorf("journal: corrupt at offset %d: bad magic", offset)
		}
		if ver := binary.BigEndian.Uint16(header[4:6]); ver != journalFrameVersion {
			return 0, 0, nil, fmt.Errorf("journal: corrupt at offset %d: unsupported frame version %d", offset, ver)
		}
		recType := header[6]
		switch recType {
		case recordTypeCreateBucket, recordTypePutObjectRoot, recordTypeDeleteObjectRoot, recordTypeDeleteBucket,
			recordTypeCreateMultipartUpload, recordTypeUploadPart, recordTypeAbortMultipartUpload, recordTypeCompleteMultipartUpload,
			recordTypePutObjectRootV2, recordTypeCompleteMultipartUploadV2, recordTypeDeleteObjectRootV2,
			recordTypePruneHistory:
		default:
			return 0, 0, nil, fmt.Errorf("journal: corrupt at offset %d: unknown record type %d", offset, recType)
		}
		seq := binary.BigEndian.Uint64(header[8:16])
		plen := binary.BigEndian.Uint32(header[16:20])
		if plen > maxJournalPayload {
			return 0, 0, nil, fmt.Errorf("journal: corrupt at offset %d: payload length %d exceeds max", offset, plen)
		}
		rest := make([]byte, int(plen)+4)
		_, rerr2 := f.ReadAt(rest, offset+journalHeaderSize)
		if rerr2 == io.EOF {
			return offset, lastSeq, records, nil
		}
		if rerr2 != nil {
			return 0, 0, nil, fmt.Errorf("journal: read failed at offset %d: %w", offset+journalHeaderSize, rerr2)
		}
		payload := rest[:plen]
		full := append(append([]byte{}, header...), payload...)
		if crc32.Checksum(full, castagnoliTable) != binary.BigEndian.Uint32(rest[plen:]) {
			return 0, 0, nil, fmt.Errorf("journal: corrupt at offset %d: crc mismatch (seq=%d)", offset, seq)
		}
		if seq != lastSeq+1 {
			return 0, 0, nil, fmt.Errorf("journal: corrupt: expected sequence %d, got %d", lastSeq+1, seq)
		}
		records = append(records, journalRecord{seq: seq, recType: recType, payload: payload})
		lastSeq = seq
		offset += int64(journalHeaderSize) + int64(plen) + 4
	}
}

// buildJournalBytes frames n records of assorted sizes (and a few unknown-length
// payloads) the way the writer does.
func buildJournalBytes(t testing.TB, rng *mrand.Rand, n int) []byte {
	var out []byte
	for i := 0; i < n; i++ {
		payload := make([]byte, rng.IntN(300))
		for k := range payload {
			payload[k] = byte(rng.IntN(256))
		}
		if rng.IntN(40) == 0 {
			payload = make([]byte, 1+rng.IntN(3<<20)) // bigger than the read buffer's refill
		}
		var h [journalHeaderSize]byte
		copy(h[0:4], journalMagic)
		binary.BigEndian.PutUint16(h[4:6], journalFrameVersion)
		h[6] = recordTypePutObjectRootV2
		binary.BigEndian.PutUint64(h[8:16], uint64(i+1))
		binary.BigEndian.PutUint32(h[16:20], uint32(len(payload)))
		crc := crc32.Update(crc32.Checksum(h[:], castagnoliTable), castagnoliTable, payload)
		out = append(out, h[:]...)
		out = append(out, payload...)
		out = binary.BigEndian.AppendUint32(out, crc)
	}
	return out
}

// TestP3_JournalReplayMatchesReference compares the streaming replay with the
// original on valid journals and on every kind of damage: truncation at every
// kind of boundary, flipped bytes and garbage tails. Same end offset, same
// records, same error text.
func TestP3_JournalReplayMatchesReference(t *testing.T) {
	rng := mrand.New(mrand.NewPCG(7, 11))
	dir := t.TempDir()
	check := func(name string, data []byte) {
		t.Helper()
		p := filepath.Join(dir, "j")
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		f, _ := os.Open(p)
		defer f.Close()
		e1, l1, r1, err1 := journalReference(f)
		e2, l2, r2, err2 := replayJournal(f)
		if (err1 == nil) != (err2 == nil) || (err1 != nil && err1.Error() != err2.Error()) {
			t.Fatalf("%s: errors differ: %v vs %v", name, err1, err2)
		}
		if err1 != nil {
			return
		}
		if e1 != e2 || l1 != l2 || len(r1) != len(r2) {
			t.Fatalf("%s: end/seq/count differ: %d/%d/%d vs %d/%d/%d", name, e1, l1, len(r1), e2, l2, len(r2))
		}
		for i := range r1 {
			if r1[i].seq != r2[i].seq || r1[i].recType != r2[i].recType || !bytes.Equal(r1[i].payload, r2[i].payload) {
				t.Fatalf("%s: record %d differs", name, i)
			}
		}
		// the validate-only form reports the same frame count
		_, _, n, _, err3 := replayJournalFn(f, false, nil)
		if err3 != nil || n != len(r1) {
			t.Fatalf("%s: validate-only: %d frames, %v; want %d", name, n, err3, len(r1))
		}
	}
	for round := 0; round < 12; round++ {
		base := buildJournalBytes(t, rng, 1+rng.IntN(60))
		check("valid", base)
		check("empty", nil)
		for k := 0; k < 40; k++ { // truncations anywhere
			check(fmt.Sprintf("trunc@%d", k), base[:rng.IntN(len(base)+1)])
		}
		for k := 0; k < 40; k++ { // flipped byte anywhere
			b := bytes.Clone(base)
			b[rng.IntN(len(b))] ^= byte(1 + rng.IntN(255))
			check("flip", b)
		}
		check("garbage tail", append(bytes.Clone(base), 1, 2, 3, 4, 5))
		check("zero tail", append(bytes.Clone(base), make([]byte, 64)...))
	}
}

// A failing apply must not hide a corrupt journal behind it: the journal error
// wins, exactly as it did when the whole journal was validated first.
func TestP3_JournalApplyErrorYieldsToJournalError(t *testing.T) {
	rng := mrand.New(mrand.NewPCG(3, 5))
	data := buildJournalBytes(t, rng, 30)
	p := filepath.Join(t.TempDir(), "j")
	os.WriteFile(p, data, 0o644)
	f, _ := os.Open(p)
	defer f.Close()
	boom := errors.New("boom")
	_, _, n, aerr, err := replayJournalFn(f, false, func(r journalRecord) error {
		if r.seq == 5 {
			return boom
		}
		return nil
	})
	if err != nil || !errors.Is(aerr, boom) || n != 30 {
		t.Fatalf("n=%d apply=%v err=%v", n, aerr, err)
	}
	bad := bytes.Clone(data)
	bad[len(bad)-9] ^= 0xff // last frame's payload or crc
	os.WriteFile(p, bad, 0o644)
	g, _ := os.Open(p)
	defer g.Close()
	_, _, _, aerr, err = replayJournalFn(g, false, func(r journalRecord) error { return boom })
	if err == nil || aerr != nil {
		t.Fatalf("journal error must win: apply=%v err=%v", aerr, err)
	}
}

// ---------------------------------------------------------------------------
// Reachability: the parallel scan against the serial reference.
// ---------------------------------------------------------------------------

// p3Fixture builds a store that has every root category: current objects of
// several sizes (1 chunk and many), shared chunks, retained history, a deleted
// key, a durable snapshot and an in-progress multipart upload.
func p3Fixture(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	big := genRandomBytes(11, 3<<20)
	for i := 0; i < 24; i++ {
		mustPutObject(t, s, "b", fmt.Sprintf("small/%02d", i), genRandomBytes(int64(100+i), 500+i*37), "text/plain", nil)
	}
	mustPutObject(t, s, "b", "dup/a", big, "application/octet-stream", map[string]string{"k": "v"})
	mustPutObject(t, s, "b", "dup/b", big, "application/octet-stream", nil)
	for r := 0; r < 4; r++ { // history
		mustPutObject(t, s, "b", "rev", genRandomBytes(int64(900+r), 70_000+r*1000), "application/octet-stream", nil)
	}
	mustPutObject(t, s, "b", "gone", genRandomBytes(5, 9000), "application/octet-stream", nil)
	if err := s.DeleteObject("b", "gone"); err != nil {
		t.Fatal(err)
	}
	entries, err := s.captureSnapshotEntries("b", "small")
	if err != nil {
		t.Fatal(err)
	}
	d := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(), CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: entries}
	if err := s.publishSnapshot(d); err != nil {
		t.Fatal(err)
	}
	mustPutObject(t, s, "b", "small/00", genRandomBytes(1234, 800), "text/plain", nil) // the snapshot now holds the old one
	id, err := s.CreateMultipartUpload("b", "mp", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UploadPart("b", "mp", id, 1, genRandomBytes(77, 300_000)); err != nil {
		t.Fatal(err)
	}
	s.sealSegments()
	return s
}

type p3Obs struct {
	scope       rootScope
	bucket, key string
	chunks      []chunkRef
}

func normIssues(r reachabilityResult) []string {
	var out []string
	for _, i := range r.Issues {
		subj := i.Subject
		if strings.HasPrefix(subj, "chunk ") { // digests are named in lowercase hex by the new scan
			subj = strings.ToLower(subj)
		}
		out = append(out, i.Kind+"|"+subj+"|"+i.Detail)
	}
	slices.Sort(out)
	return out
}

func compareReachability(t *testing.T, s *Store, deep bool, what string) {
	t.Helper()
	var o1, o2 []p3Obs
	ref, err1 := s.referenceReachability(deep, func(sc rootScope, b, k string, c []chunkRef) { o1 = append(o1, p3Obs{sc, b, k, slices.Clone(c)}) })
	got, err2 := s.computeReachabilityObserved(deep, func(sc rootScope, b, k string, c []chunkRef) { o2 = append(o2, p3Obs{sc, b, k, slices.Clone(c)}) })
	if err1 != nil || err2 != nil {
		t.Fatalf("%s: errors %v / %v", what, err1, err2)
	}
	fail := func(f string, a ...any) { t.Helper(); t.Fatalf("%s (deep=%v): "+f, append([]any{what, deep}, a...)...) }
	// The one intended difference: a digest written in upper-case hex names the
	// same chunk, and the new scan keys it by the canonical lower-case name.
	lower := func(m map[string]bool) map[string]bool {
		out := make(map[string]bool, len(m))
		for k, v := range m {
			out[strings.ToLower(k)] = v
		}
		return out
	}
	ref.ReferencedChunks, ref.ValidChunks = lower(ref.ReferencedChunks), lower(ref.ValidChunks)
	cl := make(map[string]int64, len(ref.ChunkLength))
	for k, v := range ref.ChunkLength {
		cl[strings.ToLower(k)] = v
	}
	ref.ChunkLength = cl
	if a, b := normIssues(ref), normIssues(got); !slices.Equal(a, b) {
		fail("issues differ:\nref=%q\ngot=%q", a, b)
	}
	if ref.Missing != got.Missing || ref.Corrupt != got.Corrupt || ref.Invalid != got.Invalid || ref.OK() != got.OK() {
		fail("tallies differ: %+v vs %+v", ref.issueTracker, got.issueTracker)
	}
	if ref.JournalFramesChecked != got.JournalFramesChecked || ref.JournalOK != got.JournalOK ||
		ref.CurrentRootCount != got.CurrentRootCount || ref.HistoricalRootCount != got.HistoricalRootCount ||
		ref.MultipartRootCount != got.MultipartRootCount || ref.SnapshotRootCount != got.SnapshotRootCount ||
		ref.ManifestsChecked != got.ManifestsChecked || ref.ChunksChecked != got.ChunksChecked {
		fail("counters differ:\nref=%+v\ngot=%+v", ref, got)
	}
	if !maps.Equal(ref.ReferencedManifests, got.ReferencedManifests) || !maps.Equal(ref.ManifestSHA, got.ManifestSHA) {
		fail("manifest sets differ")
	}
	if !maps.Equal(ref.ReferencedChunks, got.ReferencedChunks) {
		fail("referenced chunks differ: %d vs %d", len(ref.ReferencedChunks), len(got.ReferencedChunks))
	}
	if !maps.Equal(ref.ChunkLength, got.ChunkLength) {
		fail("chunk lengths differ")
	}
	if !maps.Equal(ref.ValidChunks, got.ValidChunks) {
		fail("valid chunks differ: %d vs %d", len(ref.ValidChunks), len(got.ValidChunks))
	}
	if ref.RepRoots != got.RepRoots || len(ref.RepFaults) != len(got.RepFaults) || len(ref.RepOrphanRoots) != len(got.RepOrphanRoots) {
		fail("representation results differ")
	}
	// The observer saw the same roots with the same chunk lists (ordering of the
	// unsorted categories is not part of the contract: compare as sets).
	key := func(o p3Obs) string {
		var b strings.Builder
		fmt.Fprintf(&b, "%d|%s|%s", o.scope, o.bucket, o.key)
		for _, c := range o.chunks {
			fmt.Fprintf(&b, "|%s:%d", strings.ToLower(c.SHA256), c.Length)
		}
		return b.String()
	}
	var k1, k2 []string
	for _, o := range o1 {
		k1 = append(k1, key(o))
	}
	for _, o := range o2 {
		k2 = append(k2, key(o))
	}
	slices.Sort(k1)
	slices.Sort(k2)
	if !slices.Equal(k1, k2) {
		fail("observer calls differ (%d vs %d)", len(k1), len(k2))
	}
	if len(ref.ReferencedChunks) == 0 && what == "healthy" {
		fail("fixture has no chunks")
	}
}

func TestP3_ReachabilityMatchesReference_Healthy(t *testing.T) {
	forEachLayout(t, func(t *testing.T) {
		s := p3Fixture(t)
		for _, workers := range []string{"1", "3", "8"} {
			t.Setenv("ZEROS3_MAINT_WORKERS", workers)
			compareReachability(t, s, false, "healthy")
			compareReachability(t, s, true, "healthy")
		}
		rr, _ := s.computeReachability(true)
		if !rr.OK() || rr.CurrentRootCount == 0 || rr.HistoricalRootCount == 0 || rr.SnapshotRootCount == 0 || rr.MultipartRootCount == 0 {
			t.Fatalf("fixture lacks a root category: %+v", rr)
		}
	})
}

// Every damage the scan can report, injected into a different copy of the same
// fixture, must be reported identically by the reference and the parallel scan.
func TestP3_ReachabilityMatchesReference_Damage(t *testing.T) {
	type damage struct {
		name string
		do   func(t *testing.T, s *Store)
	}
	pick := func(t *testing.T, s *Store, key string) (string, [32]byte, manifestV1) {
		t.Helper()
		s.mu.Lock()
		e := s.buckets["b"].objects[key]
		s.mu.Unlock()
		man, _, err := s.readManifestAny(e.manifestUUID)
		if err != nil {
			t.Fatal(err)
		}
		return e.manifestUUID, e.manifestSHA256, man
	}
	damages := []damage{
		{"missing chunk", func(t *testing.T, s *Store) {
			_, _, man := pick(t, s, "dup/a")
			removeChunkAnyLayout(t, s, chunkSumHex(t, man.Chunks[1].SHA256))
		}},
		{"corrupt chunk", func(t *testing.T, s *Store) {
			_, _, man := pick(t, s, "dup/a")
			corruptChunkAnyLayout(t, s, chunkSumHex(t, man.Chunks[0].SHA256))
		}},
		{"missing small chunk", func(t *testing.T, s *Store) {
			_, _, man := pick(t, s, "small/05")
			removeChunkAnyLayout(t, s, chunkSumHex(t, man.Chunks[0].SHA256))
		}},
		{"deleted manifest", func(t *testing.T, s *Store) {
			id, _, _ := pick(t, s, "small/03")
			deleteManifest(t, s, id)
		}},
		{"garbled manifest", func(t *testing.T, s *Store) {
			id, _, _ := pick(t, s, "small/04")
			tamperManifest(t, s, id, []byte("{not json"))
		}},
		{"deleted shared manifest of history", func(t *testing.T, s *Store) {
			s.mu.Lock()
			h := s.history["b"]["rev"][0]
			s.mu.Unlock()
			deleteManifest2(t, s, h.manifestUUID, h.manifestSHA256)
		}},
	}
	for _, d := range damages {
		t.Run(d.name, func(t *testing.T) {
			forEachLayout(t, func(t *testing.T) {
				s := p3Fixture(t)
				d.do(t, s)
				for _, workers := range []string{"1", "4"} {
					t.Setenv("ZEROS3_MAINT_WORKERS", workers)
					compareReachability(t, s, false, d.name)
					compareReachability(t, s, true, d.name)
				}
				rr, _ := s.computeReachability(true)
				if rr.OK() {
					t.Fatal("damage was not detected")
				}
			})
		})
	}
}

// deleteManifest2 removes a manifest by (uuid, journal digest), whichever
// physical form holds it.
func deleteManifest2(t *testing.T, s *Store, id string, sha [32]byte) {
	t.Helper()
	if err := os.Remove(filepath.Join(s.root, "manifests", id+".json")); err == nil {
		return
	}
	removeChunkAnyLayout(t, s, sha)
}

// Hand-written manifests exercise the validation branches that a healthy
// writer never produces: each is stored under the digest the journal recorded
// (a loose file) so the only thing under test is the structural judgement.
func TestP3_ManifestJudgementMatchesReference(t *testing.T) {
	useLooseLayout(t, "manifests are written as files so any bytes can be given a matching digest")
	valid := func(mut func(m *manifestV1)) []byte {
		sum := sha256.Sum256([]byte("x"))
		m := manifestV1{ManifestFormatVersion: manifestFormatVersion, CDCFormatVersion: cdcFormatVersion, HashAlgorithm: "sha256",
			ManifestUUID: "u", TotalLength: 10, Chunks: []chunkRef{{hex.EncodeToString(sum[:]), 4}, {hex.EncodeToString(sum[:]), 6}}, ObjectSHA256: hex.EncodeToString(sum[:])}
		if mut != nil {
			mut(&m)
		}
		b, _ := json.Marshal(m)
		return b
	}
	cases := map[string][]byte{
		"canonical":        valid(nil),
		"empty":            valid(func(m *manifestV1) { m.Chunks = nil; m.TotalLength = 0 }),
		"bad version":      valid(func(m *manifestV1) { m.ManifestFormatVersion = 99 }),
		"bad cdc":          valid(func(m *manifestV1) { m.CDCFormatVersion = 99 }),
		"bad hash":         valid(func(m *manifestV1) { m.HashAlgorithm = "md5" }),
		"short digest":     valid(func(m *manifestV1) { m.Chunks[0].SHA256 = "abcd" }),
		"two bad digests":  valid(func(m *manifestV1) { m.Chunks[0].SHA256 = "zz"; m.Chunks[1].SHA256 = "yy" }),
		"negative length":  valid(func(m *manifestV1) { m.Chunks[0].Length = -4; m.TotalLength = 2 }),
		"sum mismatch":     valid(func(m *manifestV1) { m.TotalLength = 11 }),
		"uppercase digest": []byte(strings.Replace(string(valid(nil)), "2d711642", "2D711642", -1)),
		"leading zero":     []byte(strings.Replace(string(valid(nil)), `"length":4`, `"length":04`, 1)),
		"trailing junk":    append(valid(nil), []byte(" x")...),
		"not json":         []byte("{"),
		"two chunk lists":  []byte(strings.Replace(string(valid(nil)), `"object_sha256"`, `"chunks":[],"object_sha256"`, 1)),
		"pretty printed":   []byte(strings.Replace(string(valid(nil)), `"chunks":[`, `"chunks": [ `, 1)),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			s.CreateBucket("b")
			mustPutObject(t, s, "b", "k", []byte("hello"), "text/plain", nil)
			s.mu.Lock()
			e := s.buckets["b"].objects["k"]
			s.mu.Unlock()
			// swap the stored manifest for the case's bytes and re-point the root at their digest
			sum := sha256.Sum256(data)
			if err := os.WriteFile(filepath.Join(s.root, "manifests", e.manifestUUID+".json"), data, 0o644); err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			s.buckets["b"].objects["k"] = &objectEntry{manifestUUID: e.manifestUUID, manifestSHA256: sum, size: e.size, seq: e.seq}
			s.mu.Unlock()
			os.Remove(filepath.Join(s.root, "manifests", "x")) // no-op, keeps the intent obvious
			// the chunk store may still hold the original chunk of "hello"; the digests in
			// the hand-written manifest do not exist, so chunk issues are part of the comparison
			compareReachability(t, s, false, name)
			compareReachability(t, s, true, name)
		})
	}
}
