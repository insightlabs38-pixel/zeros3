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
	"os/exec"
	"path/filepath"
	"reflect"
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
		defer func(b int) { reachBlock = b }(reachBlock)
		for _, block := range []int{1, 3, 7, 32768} { // block edges fall in every place the roots interleave
			reachBlock = block
			for _, workers := range []string{"1", "3", "8"} {
				t.Setenv("ZEROS3_MAINT_WORKERS", workers)
				compareReachability(t, s, false, "healthy")
				compareReachability(t, s, true, "healthy")
			}
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
				defer func(b int) { reachBlock = b }(reachBlock)
				for _, block := range []int{2, 32768} {
					reachBlock = block
					for _, workers := range []string{"1", "4"} {
						t.Setenv("ZEROS3_MAINT_WORKERS", workers)
						compareReachability(t, s, false, d.name)
						compareReachability(t, s, true, d.name)
					}
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

// ---------------------------------------------------------------------------
// Repack: the pipelined stager must write exactly the packs the serial one did.
// ---------------------------------------------------------------------------

func packNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(dir, "packs"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

func TestP3_RepackPipelineWritesIdenticalPacks(t *testing.T) {
	for _, compress := range []string{"off", "auto"} {
		t.Run("compression="+compress, func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			s.CreateBucket("b")
			text := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog. "), 80)
			for i := 0; i < 900; i++ {
				body := genRandomBytes(int64(i), 2500+i%700)
				if i%3 == 0 {
					body = append(bytes.Clone(text), byte(i), byte(i>>8)) // compressible
				}
				mustPutObject(t, s, "b", fmt.Sprintf("k%04d", i), body, "application/octet-stream", nil)
			}
			for i := 0; i < 900; i += 2 {
				if err := s.DeleteObject("b", fmt.Sprintf("k%04d", i)); err != nil {
					t.Fatal(err)
				}
			}
			s.Close()
			keep := 0
			if _, err := pruneHistoryStore(dir, historyPruneOptions{Bucket: "b", KeepLast: &keep}, true); err != nil {
				t.Fatal(err)
			}
			compression, _ := parseCompressionFlag(compress)
			var packSets [][]string
			for _, workers := range []string{"1", "4"} {
				clone := t.TempDir()
				copyTree(t, dir, clone)
				t.Setenv("ZEROS3_MAINT_WORKERS", workers)
				res, err := repackStore(clone, repackOptions{Tier: "all", TargetBytes: 1 << 20, MaxLivePercent: 100, Compress: compression})
				if err != nil {
					t.Fatal(err)
				}
				if res.PacksWritten == 0 || res.RecordsCopied < stageWindowRecords*2 {
					t.Fatalf("workers=%s: the pipelined path was not exercised: %+v", workers, res)
				}
				packSets = append(packSets, packNames(t, clone))
				v := mustVerify(t, clone)
				if !v.OK() {
					t.Fatalf("workers=%s: verify after repack: %+v", workers, v.Issues)
				}
				s2, err := OpenStore(clone)
				if err != nil {
					t.Fatal(err)
				}
				for i := 1; i < 900; i += 2 {
					want := genRandomBytes(int64(i), 2500+i%700)
					if i%3 == 0 {
						want = append(bytes.Clone(text), byte(i), byte(i>>8))
					}
					if _, got, err := s2.GetObject("b", fmt.Sprintf("k%04d", i)); err != nil || !bytes.Equal(got, want) {
						t.Fatalf("workers=%s: object %d unreadable after repack: %v", workers, i, err)
					}
				}
				s2.Close()
			}
			if !slices.Equal(packSets[0], packSets[1]) {
				t.Fatalf("serial and pipelined repack wrote different packs:\n%v\n%v", packSets[0], packSets[1])
			}
		})
	}
}

func mustVerify(t *testing.T, dir string) VerifyResult {
	t.Helper()
	s, err := OpenStoreShared(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := s.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// ---------------------------------------------------------------------------
// Detection: corruption present before the scan, and corruption that appears
// while it runs.
// ---------------------------------------------------------------------------

// chunkSumsOf lists the distinct chunk digests of the current objects.
func chunkSumsOf(t *testing.T, s *Store) [][32]byte {
	t.Helper()
	rr, err := s.computeReachability(false)
	if err != nil {
		t.Fatal(err)
	}
	var out [][32]byte
	for h := range rr.ReferencedChunks {
		out = append(out, chunkSumHex(t, h))
	}
	return out
}

// Damage injected after a deep scan has started, to a chunk the scan has not yet
// reached, is found by that same scan (one worker, so the order is fixed).
func TestP3_DeepVerifyFindsCorruptionInjectedMidScan(t *testing.T) {
	forEachLayout(t, func(t *testing.T) {
		t.Setenv("ZEROS3_MAINT_WORKERS", "1")
		s := p3Fixture(t)
		sums := chunkSumsOf(t, s)
		// A deep scan visits digests in physical order (pack, offset, table position):
		// damage the one it reaches last.
		rr, _ := s.reachabilityScan(false, nil)
		type pos struct {
			pack uint32
			off  uint64
			idx  int
		}
		last := pos{}
		st := s.packSnap()
		for i, k := range rr.tbl.keys {
			p := pos{0, 0, i}
			if loc, ok := st.lookup(k); ok {
				p = pos{uint32(loc.pack) + 1, loc.off, i}
			}
			if i == 0 || p.pack > last.pack || p.pack == last.pack && (p.off > last.off || p.off == last.off && p.idx > last.idx) {
				last = p
			}
		}
		victim := rr.tbl.keys[last.idx]
		calls := 0
		testHook = func(point string) {
			if point == hookReachChunkCheck {
				calls++
				if calls == 2 { // after the scan began, before it reaches the last chunk
					corruptChunkAnyLayout(t, s, victim)
				}
			}
		}
		defer func() { testHook = nil }()
		v, err := s.Verify(true)
		testHook = nil
		if err != nil {
			t.Fatal(err)
		}
		if v.OK() || v.Corrupt+v.Missing == 0 {
			t.Fatalf("corruption injected during the scan went unnoticed (%d chunks): %+v", len(sums), v.Issues)
		}
		found := false
		for _, iss := range v.Issues {
			if strings.Contains(iss.Subject, hex.EncodeToString(victim[:])) {
				found = true
			}
		}
		if !found {
			t.Fatalf("the damaged chunk %x is not named: %+v", victim, v.Issues)
		}
	})
}

// A chunk that rots between the chunk pass and the whole-object pass is still
// found: multi-chunk objects are read (and hashed) again.
func TestP3_DeepVerifyFindsCorruptionBetweenPasses(t *testing.T) {
	forEachLayout(t, func(t *testing.T) {
		t.Setenv("ZEROS3_MAINT_WORKERS", "1")
		s := p3Fixture(t)
		s.mu.Lock()
		e := s.buckets["b"].objects["dup/a"]
		s.mu.Unlock()
		man, _, err := s.readManifestAny(e.manifestUUID)
		if err != nil || len(man.Chunks) < 2 {
			t.Fatalf("fixture needs a multi-chunk object: %v %d", err, len(man.Chunks))
		}
		victim := chunkSumHex(t, man.Chunks[1].SHA256)
		done := false
		testHook = func(point string) {
			if point == hookVerifyWholeObj && !done {
				done = true
				corruptChunkAnyLayout(t, s, victim)
			}
		}
		defer func() { testHook = nil }()
		v, err := s.Verify(true)
		testHook = nil
		if err != nil {
			t.Fatal(err)
		}
		if v.OK() {
			t.Fatal("damage between the chunk pass and the whole-object pass went unnoticed")
		}
	})
}

// A corrupt reachable manifest makes destructive maintenance refuse, whatever
// the worker count: nothing is deleted and gc says why.
func TestP3_GCFailsClosedOnCorruptManifest(t *testing.T) {
	for _, workers := range []string{"1", "4"} {
		t.Run("workers="+workers, func(t *testing.T) {
			forEachLayout(t, func(t *testing.T) {
				t.Setenv("ZEROS3_MAINT_WORKERS", workers)
				s := p3Fixture(t)
				s.mu.Lock()
				e := s.buckets["b"].objects["small/07"]
				s.mu.Unlock()
				tamperManifest(t, s, e.manifestUUID, []byte("{broken"))
				dir := s.root
				s.Close()
				before := packNames(t, dir)
				res, err := gcCollect(dir, true)
				if !errors.Is(err, errGCUnsafe) {
					t.Fatalf("gc -apply on a store with a corrupt live manifest: err=%v res=%+v", err, res)
				}
				if res.LiveSetOK {
					t.Fatal("live set reported ok")
				}
				if !slices.Equal(before, packNames(t, dir)) {
					t.Fatal("gc removed packs despite the refusal")
				}
				// everything else is still readable
				s2, err := OpenStore(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer s2.Close()
				if _, _, err := s2.GetObject("b", "dup/b"); err != nil {
					t.Fatalf("an unrelated object was lost: %v", err)
				}
			})
		})
	}
}

// ---------------------------------------------------------------------------
// Journal fast decoders: whatever they accept, encoding/json decodes the same.
// ---------------------------------------------------------------------------

func p3RandString(rng *mrand.Rand) string {
	pool := []string{"", "a", "bench", "k/00000042", "dir/sub dir/file.txt", "ünïcödé/文件", "quote\"inside", "back\\slash", "tab\there", "nl\nx",
		"</script>&amp;", "\u2028", "emoji😀", "ctl\x01", "\xff\xfe", "{\"x\":1}", "x,y:z", "null", "-1"}
	if rng.IntN(5) < 3 { // mostly the shapes the writer produces
		return []string{"", "bench", "k/00000042", "dir/sub dir/file.txt", "ünïcödé/文件", "application/octet-stream", "d41d8cd98f00b204e9800998ecf8427e"}[rng.IntN(7)]
	}
	if rng.IntN(3) == 0 {
		b := make([]byte, rng.IntN(40))
		for i := range b {
			b[i] = byte(32 + rng.IntN(95))
		}
		return string(b)
	}
	return pool[rng.IntN(len(pool))]
}

func p3RandArchived(rng *mrand.Rand) journalArchivedVersionPayload {
	return journalArchivedVersionPayload{VersionID: newUUIDv7(), ManifestUUID: newUUIDv7(), ManifestSHA256: strings.Repeat("ab", 32), Size: rng.Int64N(1 << 40),
		ETag: p3RandString(rng), ContentType: p3RandString(rng),
		ArchivedAt: time.Unix(rng.Int64N(2e9), rng.Int64N(1e9)).In([]*time.Location{time.UTC, time.FixedZone("x", 3600*rng.IntN(12))}[rng.IntN(2)]),
		Reason:     []string{historyReasonOverwritten, historyReasonDeleted, p3RandString(rng)}[rng.IntN(3)]}
}

func TestP3_FastJournalDecodeMatchesJSON(t *testing.T) {
	rng := mrand.New(mrand.NewPCG(21, 22))
	accepted, rejected := 0, 0
	checkPut := func(b []byte) {
		var fast, ref journalPutPayloadV2
		okFast := decodePutV2Fast(b, &fast, &strIntern{m: map[string]string{}})
		errRef := json.Unmarshal(b, &ref)
		if okFast {
			accepted++
			if errRef != nil || !reflect.DeepEqual(fast, ref) {
				t.Fatalf("fast path accepted %q but json says %v:\nfast=%+v\n ref=%+v", b, errRef, fast, ref)
			}
		} else {
			rejected++
		}
	}
	checkDel := func(b []byte) {
		var fast, ref journalDeleteObjectPayloadV2
		okFast := decodeDeleteV2Fast(b, &fast, nil)
		errRef := json.Unmarshal(b, &ref)
		if okFast {
			accepted++
			if errRef != nil || !reflect.DeepEqual(fast, ref) {
				t.Fatalf("fast path accepted %q but json says %v:\nfast=%+v\n ref=%+v", b, errRef, fast, ref)
			}
		} else {
			rejected++
		}
	}
	mutate := func(b []byte) []byte {
		b = bytes.Clone(b)
		switch rng.IntN(7) {
		case 0:
			b[rng.IntN(len(b))] = byte(rng.IntN(256))
		case 1:
			i := rng.IntN(len(b))
			b = append(b[:i], b[i+1:]...)
		case 2:
			i := rng.IntN(len(b))
			b = append(b[:i], append([]byte{' '}, b[i:]...)...)
		case 3:
			b = b[:rng.IntN(len(b))]
		case 4:
			b = bytes.Replace(b, []byte(`"bucket"`), []byte(`"Bucket"`), 1)
		case 5:
			b = bytes.Replace(b, []byte(`"size"`), []byte(`"etag":"dup","size"`), 1)
		case 6:
			b = bytes.Replace(b, []byte(`"size":`), []byte(`"size":0`), 1)
		}
		return b
	}
	for i := 0; i < 6000; i++ {
		p := journalPutPayloadV2{Bucket: p3RandString(rng), Key: p3RandString(rng), ManifestUUID: newUUIDv7(), ManifestSHA256: strings.Repeat("0f", 32),
			Size: rng.Int64N(1<<40) - rng.Int64N(2), ETag: p3RandString(rng), ContentType: p3RandString(rng), VersionID: newUUIDv7()}
		if rng.IntN(2) == 0 {
			a := p3RandArchived(rng)
			p.Previous = &a
		}
		b, _ := json.Marshal(p)
		checkPut(b)
		checkPut(mutate(b))
		d := journalDeleteObjectPayloadV2{Bucket: p3RandString(rng), Key: p3RandString(rng), Archived: p3RandArchived(rng)}
		b, _ = json.Marshal(d)
		checkDel(b)
		checkDel(mutate(b))
	}
	if accepted < 5000 || rejected == 0 {
		t.Fatalf("the generator no longer exercises both outcomes: accepted %d rejected %d", accepted, rejected)
	}
	t.Logf("accepted %d, fell back %d", accepted, rejected)
}

// ---------------------------------------------------------------------------
// Repack under many small records: crash points and real kills.
// ---------------------------------------------------------------------------

// p3ChurnedStore writes n small objects, deletes about 60% of them (keeping the
// rest), drops the retained history and closes the store, leaving packs that are
// mostly dead. It returns the directory and the objects that must survive.
func p3ChurnedStore(t *testing.T, n int) (string, map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.CreateBucket("b")
	live := map[string][]byte{}
	for i := 0; i < n; i++ {
		body := genRandomBytes(int64(1000+i), 2000+i%1500)
		if i%2 == 0 {
			body = append(bytes.Repeat([]byte("compressible text "), 100), byte(i), byte(i>>8))
		}
		k := fmt.Sprintf("k%05d", i)
		mustPutObject(t, s, "b", k, body, "application/octet-stream", nil)
		if i%5 < 3 {
			if err := s.DeleteObject("b", k); err != nil {
				t.Fatal(err)
			}
		} else {
			live[k] = body
		}
	}
	s.Close()
	keep := 0
	if _, err := pruneHistoryStore(dir, historyPruneOptions{Bucket: "b", KeepLast: &keep}, true); err != nil {
		t.Fatal(err)
	}
	return dir, live
}

func requireP3Live(t *testing.T, dir string, live map[string][]byte, stage string) {
	t.Helper()
	if v := mustVerify(t, dir); !v.OK() {
		t.Fatalf("%s: verify -deep: %+v", stage, v.Issues)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("%s: reopen: %v", stage, err)
	}
	defer s.Close()
	for k, want := range live {
		if _, got, err := s.GetObject("b", k); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: acknowledged object %s lost or changed: %v", stage, k, err)
		}
	}
}

func TestP3_RepackCrashPointsWithPipelinedStaging(t *testing.T) {
	base, live := p3ChurnedStore(t, 800)
	opt := repackOptions{Tier: "all", TargetBytes: 256 << 10, MaxLivePercent: 100, Compress: true}
	cases := []struct {
		name  string
		point string
		nth   int
	}{
		{"first record appended", hookPackRecordWritten, 1},
		{"inside the second window", hookPackRecordWritten, stageWindowRecords + 20},
		{"before fsync", hookPackBeforeSync, 1},
		{"validated", hookPackValidated, 1},
		{"before rename", hookPackBeforePublish, 1},
		{"after rename", hookPackAfterRename, 1},
		{"first replacement published", hookPackPublished, 1},
		{"second replacement mid-write", hookPackBeforeSync, 2},
		{"before obsolete removal", hookBeforePackDelete, 1},
		{"between removals", hookBeforePackDelete, 2},
		{"removed, before dir fsync", hookPackDeleted, 1},
		{"completed", hookRepackDone, 1},
	}
	for _, workers := range []string{"1", "4"} {
		for _, c := range cases {
			t.Run("workers="+workers+"/"+c.name, func(t *testing.T) {
				t.Setenv("ZEROS3_MAINT_WORKERS", workers)
				dir := t.TempDir()
				copyTree(t, base, dir)
				calls := 0
				withTestHook(t, func(point string) {
					if point == c.point {
						calls++
						if calls == c.nth {
							panic(simulatedCrash{point: point})
						}
					}
				})
				runExpectingSimulatedCrash(t, func() { _, _ = repackStore(dir, opt) })
				testHook = nil
				requireP3Live(t, dir, live, "after the crash")
				if _, err := repackStore(dir, opt); err != nil {
					t.Fatalf("rerun: %v", err)
				}
				requireP3Live(t, dir, live, "after the rerun")
				if stale, _ := filepath.Glob(filepath.Join(dir, "tmp", "pack-*.tmp")); len(stale) != 0 {
					t.Fatalf("stale staging files remain: %v", stale)
				}
				if again, err := repackStore(dir, opt); err != nil || again.PacksSelected != 0 {
					t.Fatalf("a converged store must select nothing: %+v %v", again, err)
				}
			})
		}
	}
}

// TestP3_MaintHelper is the child of the real-kill test: it runs one maintenance
// command to completion unless it is killed first.
func TestP3_MaintHelper(t *testing.T) {
	spec := os.Getenv("ZEROS3_P3_HELPER")
	if spec == "" {
		t.Skip("child process of TestP3_RealKillDuringMaintenance")
	}
	op, dir, _ := strings.Cut(spec, ":")
	var err error
	switch op {
	case "repack":
		_, err = repackStore(dir, repackOptions{Tier: "all", TargetBytes: 256 << 10, MaxLivePercent: 100, Compress: true})
	case "gc":
		_, err = gcCollect(dir, true)
	}
	if err != nil {
		os.Exit(3)
	}
}

func TestP3_RealKillDuringMaintenance(t *testing.T) {
	base, live := p3ChurnedStore(t, 1200)
	for _, op := range []string{"repack", "gc"} {
		t.Run(op, func(t *testing.T) {
			killed := 0
			dir := t.TempDir()
			copyTree(t, base, dir)
			rng := mrand.New(mrand.NewPCG(99, uint64(len(op))))
			for round := 0; round < 10; round++ {
				cmd := exec.Command(os.Args[0], "-test.run=^TestP3_MaintHelper$")
				cmd.Env = append(os.Environ(), "ZEROS3_P3_HELPER="+op+":"+dir)
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				select {
				case <-done: // finished before the kill: converged
				case <-time.After(time.Duration(10+rng.IntN(250)) * time.Millisecond):
					cmd.Process.Kill()
					<-done
					killed++
				}
				requireP3Live(t, dir, live, fmt.Sprintf("round %d", round))
			}
			// whatever was interrupted, the store converges when left alone
			var err error
			if op == "repack" {
				_, err = repackStore(dir, repackOptions{Tier: "all", TargetBytes: 256 << 10, MaxLivePercent: 100, Compress: true})
			} else {
				_, err = gcCollect(dir, true)
			}
			if err != nil {
				t.Fatal(err)
			}
			requireP3Live(t, dir, live, "final")
			t.Logf("%s: %d of 10 runs were killed", op, killed)
			if killed == 0 {
				t.Log("note: no run was killed in flight on this machine (all finished first)")
			}
		})
	}
}
