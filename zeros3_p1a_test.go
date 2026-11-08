package main

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"testing"
	"testing/iotest"
)

// refFindCDCBoundary is the original, branch-per-byte CDC v1 scan. The
// production scan must return exactly its cut for every input.
func refFindCDCBoundary(data []byte, eof bool) int {
	n := len(data)
	limit := n
	if limit > cdcMaxChunkSize {
		limit = cdcMaxChunkSize
	}
	var hash uint64
	for i := 0; i < limit; i++ {
		hash = (hash << 1) + gearTable[data[i]]
		size := i + 1
		if size >= cdcMaxChunkSize {
			return size
		}
		if size < cdcMinChunkSize {
			continue
		}
		mask := uint64(cdcMaskL)
		if size < cdcTargetChunkSize {
			mask = cdcMaskS
		}
		if hash&mask == 0 {
			return size
		}
	}
	return n
}

func p1aRand(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	b := make([]byte, n)
	for i := 0; i+8 <= n; i += 8 {
		v := r.Uint64()
		for j := 0; j < 8; j++ {
			b[i+j] = byte(v >> (8 * j))
		}
	}
	return b
}

func TestP1A_CDCBoundaryMatchesReference(t *testing.T) {
	inputs := map[string][]byte{
		"random":   p1aRand(1, 40<<20),
		"zeros":    make([]byte, 3<<20),
		"ones":     bytes.Repeat([]byte{0xff}, 3<<20),
		"periodic": bytes.Repeat([]byte("0123456789abcdef"), 1<<18),
		"text":     bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 1<<16),
	}
	// A low-entropy mix gives many cuts at unusual offsets.
	lo := p1aRand(2, 8<<20)
	for i := range lo {
		lo[i] &= 3
	}
	inputs["lowentropy"] = lo
	for name, data := range inputs {
		for _, eof := range []bool{false, true} {
			off, cuts := 0, 0
			for off < len(data) {
				w := data[off:min(len(data), off+cdcMaxChunkSize)]
				want, got := refFindCDCBoundary(w, eof), findCDCBoundary(w, eof)
				if want != got {
					t.Fatalf("%s eof=%v offset %d: cut %d, reference %d", name, eof, off, got, want)
				}
				off += want
				cuts++
			}
			t.Logf("%s eof=%v: %d identical cuts", name, eof, cuts)
		}
	}
	// Every window length around the min/target/max boundaries and random lengths.
	rnd := rand.New(rand.NewPCG(7, 8))
	buf := p1aRand(3, 2*cdcMaxChunkSize)
	lens := []int{0, 1, 2, 15, 16, 17, cdcMinChunkSize - 17, cdcMinChunkSize - 16, cdcMinChunkSize - 15, cdcMinChunkSize - 1, cdcMinChunkSize, cdcMinChunkSize + 1,
		cdcTargetChunkSize - 1, cdcTargetChunkSize, cdcTargetChunkSize + 1, cdcMaxChunkSize - 1, cdcMaxChunkSize, cdcMaxChunkSize + 1, 2 * cdcMaxChunkSize}
	for i := 0; i < 2000; i++ {
		lens = append(lens, rnd.IntN(2*cdcMaxChunkSize))
	}
	for _, l := range lens {
		for _, start := range []int{0, 1, 4093} {
			if start+l > len(buf) {
				continue
			}
			w := buf[start : start+l]
			if want, got := refFindCDCBoundary(w, true), findCDCBoundary(w, true); want != got {
				t.Fatalf("window len %d start %d: cut %d, reference %d", l, start, got, want)
			}
		}
	}
}

func BenchmarkP1A_CDCBoundary(b *testing.B) {
	data := p1aRand(5, 64<<20)
	for _, impl := range []struct {
		name string
		f    func([]byte, bool) int
	}{{"reference", refFindCDCBoundary}, {"current", findCDCBoundary}} {
		b.Run(impl.name, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				off := 0
				for off < len(data) {
					off += impl.f(data[off:min(len(data), off+cdcMaxChunkSize)], false)
				}
			}
		})
	}
}

// serialIngest runs the unmodified serial path regardless of size.
func serialIngest(t testing.TB, s *Store, r io.Reader, md5on bool) ingestResult {
	t.Helper()
	old := ingestAblate.noPipe
	ingestAblate.noPipe = true
	defer func() { ingestAblate.noPipe = old }()
	res, err := s.ingestStream(r, md5on, ingestOptions{size: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func sameIngest(t *testing.T, name string, got, want ingestResult) {
	t.Helper()
	if got.size != want.size || got.objSHA256 != want.objSHA256 || got.etagMD5 != want.etagMD5 || len(got.chunks) != len(want.chunks) {
		t.Fatalf("%s: size %d/%d chunks %d/%d obj %x/%x etag %x/%x", name, got.size, want.size, len(got.chunks), len(want.chunks), got.objSHA256[:4], want.objSHA256[:4], got.etagMD5[:4], want.etagMD5[:4])
	}
	for i := range got.chunks {
		if got.chunks[i] != want.chunks[i] {
			t.Fatalf("%s: chunk %d differs: %v vs %v", name, i, got.chunks[i], want.chunks[i])
		}
	}
}

// Independent digests of the reconstructed stream, not of either ingest path.
func checkDigests(t *testing.T, name string, data []byte, res ingestResult, md5on bool) {
	t.Helper()
	if res.size != int64(len(data)) || res.objSHA256 != sha256.Sum256(data) {
		t.Fatalf("%s: size/object digest wrong", name)
	}
	if md5on && res.etagMD5 != md5.Sum(data) {
		t.Fatalf("%s: md5 wrong", name)
	}
	off := int64(0)
	for i, c := range res.chunks {
		sum := sha256.Sum256(data[off : off+c.Length])
		if hex.EncodeToString(sum[:]) != c.SHA256 {
			t.Fatalf("%s: chunk %d digest wrong", name, i)
		}
		off += c.Length
	}
	if off != int64(len(data)) {
		t.Fatalf("%s: chunks cover %d of %d bytes", name, off, len(data))
	}
}

func TestP1A_PipelinedIngestMatchesSerial(t *testing.T) {
	s := newTestStore(t)
	sizes := []int{0, 1, 4095, pipeMinBytes - 1, pipeMinBytes, pipeBatchBytes, pipeBufBytes - 1, pipeBufBytes, pipeBufBytes + 1,
		2*pipeBufBytes + 12345, 9<<20 + 77, 24<<20 + 3}
	readers := map[string]func(io.Reader) io.Reader{
		"plain":   func(r io.Reader) io.Reader { return r },
		"onebyte": func(r io.Reader) io.Reader { return iotest.OneByteReader(r) },
		"half":    func(r io.Reader) io.Reader { return iotest.HalfReader(r) },
		"dataerr": func(r io.Reader) io.Reader { return iotest.DataErrReader(r) },
	}
	for _, n := range sizes {
		data := p1aRand(uint64(n)+11, n)
		for _, md5on := range []bool{true, false} {
			want := serialIngest(t, s, bytes.NewReader(data), md5on)
			checkDigests(t, fmt.Sprint("serial ", n), data, want, md5on)
			for name, wrap := range readers {
				if name == "onebyte" && n > 1<<20 {
					continue // quadratic in read calls; covered at smaller sizes
				}
				got, err := s.ingestPipelined(wrap(bytes.NewReader(data)), md5on, ingestOptions{size: int64(n)})
				if err != nil {
					t.Fatalf("size %d %s: %v", n, name, err)
				}
				sameIngest(t, fmt.Sprintf("size %d %s md5=%v", n, name, md5on), got, want)
			}
		}
	}
}

type failingReader struct {
	r     io.Reader
	after int
	err   error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.after <= 0 {
		return 0, f.err
	}
	if len(p) > f.after {
		p = p[:f.after]
	}
	n, err := f.r.Read(p)
	f.after -= n
	return n, err
}

func TestP1A_PipelinedIngestReadErrorAndCleanup(t *testing.T) {
	s := newTestStore(t)
	boom := errors.New("boom")
	data := p1aRand(99, 20<<20)
	for _, after := range []int{0, 1, pipeBufBytes, 5<<20 + 17, 19 << 20} {
		_, err := s.ingestPipelined(&failingReader{r: bytes.NewReader(data), after: after, err: boom}, true, ingestOptions{size: int64(len(data))})
		if !errors.Is(err, boom) {
			t.Fatalf("after %d: err %v", after, err)
		}
	}
}

func p1aBuildPack(t *testing.T, s *Store, seed uint64, sizes []int) (string, []packEntry, [32]byte) {
	t.Helper()
	b, err := s.newPackBuilder(tierHot, nil, len(sizes))
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range sizes {
		c := p1aRand(seed+uint64(i), n)
		b.Add(sha256.Sum256(c), c)
	}
	path, entries, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return path, entries, b.sum
}

// The exact verifier must reject every corruption the full verifier rejects.
// Flipping one bit anywhere in the file is caught by both: all header, index
// and footer bytes, the record headers, payload edges, and random positions
// (deterministic seed).
func TestP1A_VerifyPackExactEquivalentToFull(t *testing.T) {
	s := newTestStore(t)
	sizes := []int{1, 2, 17, 4000, 65536, 300, 70000, 33, 255, 12345, 262144, 999}
	path, entries, sum := p1aBuildPack(t, s, 500, sizes)
	if _, _, err := verifyPackFile(path); err != nil {
		t.Fatalf("full verify of a good pack: %v", err)
	}
	if _, err := verifyPackExact(path, entries, sum); err != nil {
		t.Fatalf("exact verify of a good pack: %v", err)
	}
	if _, err := verifyPackExact(path, entries, [32]byte{1}); err == nil {
		t.Fatal("exact verify accepted a different body digest")
	}
	good, _ := os.ReadFile(path)
	size := len(good)
	var pos []int
	for i := 0; i < packHeaderSize; i++ {
		pos = append(pos, i)
	}
	indexOff := int(entries[len(entries)-1].off) + int(entries[len(entries)-1].stored)
	for i := indexOff; i < size; i++ { // whole index and footer
		pos = append(pos, i)
	}
	for _, e := range entries {
		hdr := int(e.off) - packRecordHeaderSize
		for j := 0; j < packRecordHeaderSize; j++ {
			pos = append(pos, hdr+j)
		}
		pos = append(pos, int(e.off), int(e.off)+int(e.stored)/2, int(e.off)+int(e.stored)-1)
	}
	rnd := rand.New(rand.NewPCG(42, 43))
	for i := 0; i < 1500; i++ {
		pos = append(pos, rnd.IntN(size))
	}
	dir := t.TempDir()
	bad := dir + "/bad.pack"
	for n, p := range pos {
		mut := append([]byte(nil), good...)
		mut[p] ^= 1 << uint(n%8)
		if err := os.WriteFile(bad, mut, 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, fullErr := verifyPackFile(bad)
		_, exactErr := verifyPackExact(bad, entries, sum)
		if fullErr == nil || exactErr == nil {
			t.Fatalf("flip at byte %d of %d: full=%v exact=%v (both must reject)", p, size, fullErr, exactErr)
		}
	}
	// Truncation and extension at every structural boundary.
	for _, n := range []int{size - 1, size - packFooterSize, indexOff, int(entries[0].off), packHeaderSize, size + 1} {
		mut := append([]byte(nil), good...)
		if n < size {
			mut = mut[:n]
		} else {
			mut = append(mut, 0)
		}
		os.WriteFile(bad, mut, 0o644)
		_, _, fullErr := verifyPackFile(bad)
		_, exactErr := verifyPackExact(bad, entries, sum)
		if fullErr == nil || exactErr == nil {
			t.Fatalf("length %d: full=%v exact=%v", n, fullErr, exactErr)
		}
	}
	t.Logf("%d single-bit corruptions rejected by both verifiers", len(pos))
}

// A pack whose file differs from the entries the writer recorded is rejected
// even if it is itself a valid pack.
func TestP1A_VerifyPackExactRejectsOtherValidPack(t *testing.T) {
	s := newTestStore(t)
	p1, e1, sum1 := p1aBuildPack(t, s, 1, []int{1000, 2000, 3000})
	_, e2, _ := p1aBuildPack(t, s, 9, []int{1000, 2000, 3000})
	if _, err := verifyPackExact(p1, e2, sum1); err == nil {
		t.Fatal("exact verify accepted entries from another pack")
	}
	_ = e1
}

func TestP1A_PackWriterMatchesReferenceAndReportsErrors(t *testing.T) {
	for _, async := range []bool{true, false} {
		old := ingestAblate.syncWrite
		ingestAblate.syncWrite = !async
		dir := t.TempDir()
		f, _ := os.Create(dir + "/w")
		w := newPackWriter(f)
		if w.async != async && !(async && w.async == false) {
			t.Fatalf("async=%v got %v", async, w.async)
		}
		rnd := rand.New(rand.NewPCG(5, 6))
		var all []byte
		for i := 0; i < 400; i++ {
			n := []int{0, 1, 44, 4096, 70000, packWriteBufBytes - 1, packWriteBufBytes, packWriteBufBytes + 1, 1<<20 + 5}[rnd.IntN(9)]
			p := p1aRand(uint64(i), n)
			w.Write(p)
			all = append(all, p...)
			if i%37 == 0 {
				if err := w.Flush(); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		if w.Sum() != sha256.Sum256(all) {
			t.Fatal("body digest differs from the reference")
		}
		w.close()
		w.close()
		f.Close()
		if got, _ := os.ReadFile(dir + "/w"); !bytes.Equal(got, all) {
			t.Fatal("file differs from the bytes written")
		}
		// A failing file surfaces at Flush, and close never hangs.
		f2, _ := os.Create(dir + "/x")
		w2 := newPackWriter(f2)
		f2.Close()
		w2.Write(p1aRand(1, 3*packWriteBufBytes))
		if err := w2.Flush(); err == nil {
			t.Fatal("write to a closed file was not reported")
		}
		w2.close()
		ingestAblate.syncWrite = old
	}
}
