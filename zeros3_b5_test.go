package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// B5: restartable pack records and reconstruction-ordered layouts.

// b5Text is source-like text (about 3x compressible with DEFLATE) that varies with seed.
func b5Text(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, 0xb5))
	vocab := make([]string, 4096)
	for i := range vocab {
		w := make([]byte, 3+r.IntN(9))
		for j := range w {
			w[j] = byte('a' + r.IntN(26))
		}
		vocab[i] = string(w)
	}
	var b bytes.Buffer
	for b.Len() < n {
		for k := r.IntN(10) + 2; k > 0; k-- {
			b.WriteString(vocab[int(r.ExpFloat64()*300)%len(vocab)])
			b.WriteByte(' ')
		}
		b.WriteString("= ")
		b.WriteString(vocab[r.IntN(len(vocab))])
		b.WriteString(";\n")
	}
	return b.Bytes()[:n]
}

// b5Mutate returns a revision of base: a few lines replaced, inserted and deleted.
func b5Mutate(base []byte, seed uint64, edits int) []byte {
	r := rand.New(rand.NewPCG(seed, 0x5b))
	out := slices.Clone(base)
	for range edits {
		at := r.IntN(len(out))
		switch r.IntN(3) {
		case 0:
			out = slices.Insert(out, at, b5Text(r.Uint64(), 200+r.IntN(2000))...)
		case 1:
			out = slices.Delete(out, at, min(len(out), at+100+r.IntN(1500)))
		default:
			copy(out[at:], b5Text(r.Uint64(), min(len(out)-at, 300)))
		}
	}
	return out
}

func b5PutAll(t *testing.T, s *Store, objs map[string][]byte) {
	t.Helper()
	if err := s.CreateBucket("b"); err != nil && s.HeadBucket("b") != nil {
		t.Fatal(err)
	}
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range objs {
			if !yield(k) {
				return
			}
		}
	}) {
		if _, err := s.PutObject("b", k, objs[k], "application/octet-stream", nil); err != nil {
			t.Fatal(err)
		}
	}
}

func b5CheckAll(t *testing.T, s *Store, objs map[string][]byte) {
	t.Helper()
	r := rand.New(rand.NewPCG(7, 7))
	for k, want := range objs {
		_, got, err := s.GetObject("b", k)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: whole read inexact (%v)", k, err)
		}
		for range 24 {
			n := min(int64(len(want)), int64(1+r.IntN(80<<10)))
			off := r.Int64N(int64(len(want)) - n + 1)
			_, _, got, err := s.GetObjectRange("b", k, byteRange{off, off + n - 1})
			if err != nil || !bytes.Equal(got, want[off:off+n]) {
				t.Fatalf("%s: range %d+%d inexact (%v)", k, off, n, err)
			}
		}
	}
}

func b5BlockedLocs(s *Store) (blocked []packLoc, sums [][32]byte) {
	for sum, loc := range s.packSnap().primaries() {
		if loc.codec == packCodecBlocked {
			blocked, sums = append(blocked, loc), append(sums, *sum)
		}
	}
	return
}

func TestB5RecordBlocksFlag(t *testing.T) {
	for v, want := range map[string]uint8{"off": 0, "4": 12, "32": 15, "128": 17} {
		if got, err := parseRecordBlocksFlag(v); err != nil || got != want {
			t.Fatalf("%s: %d %v", v, got, err)
		}
	}
	for _, v := range []string{"0", "3", "48", "256", "x"} {
		if _, err := parseRecordBlocksFlag(v); err == nil {
			t.Fatalf("%s accepted", v)
		}
	}
}

// TestB5BlockedRoundTrip compacts compressible objects into restartable records
// and proves whole reads, ranges, spans, verify -deep and a reopen.
func TestB5BlockedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	objs := map[string][]byte{"a": b5Text(1, 3<<20), "b": b5Text(2, 900<<10), "small": b5Text(3, 20<<10), "rand": genRandomBytes(4, 600<<10)}
	b5PutAll(t, s, objs)
	s.Close()
	res := b5Repack(t, dir, repackOptions{TargetBytes: 2 << 20, BlockLog: 15})
	if s, err = OpenStore(dir); err != nil {
		t.Fatal(err)
	}
	blocked, sums := b5BlockedLocs(s)
	if len(blocked) == 0 || res.PacksWritten == 0 {
		t.Fatalf("no blocked records: %+v", res)
	}
	if !slices.Contains(s.formatSnapshot().Features, packBlockedFeature) {
		t.Fatal("store feature not recorded")
	}
	b5CheckAll(t, s, objs)
	// Spans: every block boundary straddle and the record ends.
	st := s.packSnap()
	for i, loc := range blocked[:min(8, len(blocked))] {
		whole, err := s.casRead(sums[i])
		if err != nil {
			t.Fatal(err)
		}
		for _, sp := range [][2]int64{{0, 1}, {32<<10 - 1, 2}, {40 << 10, 4096}, {int64(loc.logical) - 1, 1}, {0, int64(loc.logical)}} {
			if sp[1] = min(sp[1], int64(loc.logical)-sp[0]); sp[1] <= 0 {
				continue
			}
			data, at, err := st.readPackedSpan(sums[i], loc, sp[0], sp[1], nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if at > sp[0] || at+int64(len(data)) < sp[0]+sp[1] || !bytes.Equal(data, whole[at:at+int64(len(data))]) {
				t.Fatalf("span %v of %x: got [%d,%d)", sp, sums[i][:4], at, at+int64(len(data)))
			}
		}
	}
	vr, err := s.Verify(true)
	if err != nil || len(vr.Issues) != 0 {
		t.Fatalf("verify -deep: %v %+v", err, vr.Issues)
	}
	s.Close()
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	b5CheckAll(t, s2, objs)
}

// b5Repack rewrites every pack of the closed store at dir with opt (compressing, all packs selected).
func b5Repack(t *testing.T, dir string, opt repackOptions) RepackResult {
	t.Helper()
	opt.MaxLivePercent, opt.Compress, opt.Recompress = 100, true, true
	if opt.TargetBytes == 0 {
		opt.TargetBytes = defaultPackTargetBytes
	}
	res, err := repackStore(dir, opt)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// b5Blocked builds a store with one compressible object packed as restartable records.
func b5Blocked(t *testing.T) (string, []byte) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	data := b5Text(9, 2<<20)
	b5PutAll(t, s, map[string][]byte{"a": data})
	s.Close()
	b5Repack(t, dir, repackOptions{BlockLog: 15})
	return dir, data
}

func b5OnlyPack(t *testing.T, dir string) (string, []packEntry) {
	t.Helper()
	ps, _ := filepath.Glob(filepath.Join(dir, "packs", "*.pack"))
	if len(ps) != 1 {
		t.Fatalf("packs: %v", ps)
	}
	_, entries, err := loadPackFile(ps[0])
	if err != nil {
		t.Fatal(err)
	}
	return ps[0], entries
}

func b5Flip(t *testing.T, path string, off int64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var b [1]byte
	f.ReadAt(b[:], off)
	b[0] ^= 0x40
	if _, err := f.WriteAt(b[:], off); err != nil {
		t.Fatal(err)
	}
}

// TestB5BlockedCorruption: a damaged block fails every read of it (never wrong
// bytes); a damaged table digest fails the span but the whole read, which checks
// the chunk digest, still serves; verify -deep reports both.
func TestB5BlockedCorruption(t *testing.T) {
	for _, where := range []string{"block", "table", "header"} {
		t.Run(where, func(t *testing.T) {
			dir, data := b5Blocked(t)
			path, entries := b5OnlyPack(t, dir)
			var e packEntry
			for _, x := range entries {
				if x.codec == packCodecBlocked {
					e = x
					break
				}
			}
			switch where {
			case "block":
				b5Flip(t, path, int64(e.off)+int64(e.stored)-10)
			case "table":
				b5Flip(t, path, int64(e.off)+packBlockHeader+4+3)
			case "header":
				b5Flip(t, path, int64(e.off)+2) // block count
			}
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			loc, _ := s.packLookup(e.sha)
			_, _, spanErr := s.packSnap().readPackedSpan(e.sha, loc, 0, 100, nil, nil)
			_, wholeErr := s.casRead(e.sha)
			if where == "table" {
				if spanErr == nil || wholeErr != nil {
					t.Fatalf("table damage: span %v, whole %v", spanErr, wholeErr)
				}
			} else if wholeErr == nil {
				t.Fatalf("%s damage served a chunk", where)
			}
			// Served ranges never return wrong bytes.
			for off := int64(0); off < int64(len(data)); off += 7919 {
				if _, _, got, err := s.GetObjectRange("b", "a", byteRange{off, off + 99}); err == nil && !bytes.Equal(got, data[off:off+100]) {
					t.Fatalf("wrong bytes at %d", off)
				}
			}
			vr, err := s.Verify(true)
			if err != nil {
				t.Fatal(err)
			}
			if len(vr.Issues) == 0 {
				t.Fatalf("verify -deep missed %s damage", where)
			}
		})
	}
}

// TestB5DictionaryCopy: losing the cited dictionary record is survived through its second copy;
// losing both fails closed.
func TestB5DictionaryCopy(t *testing.T) {
	dir, data := b5Blocked(t)
	path, entries := b5OnlyPack(t, dir)
	first, last := entries[0], entries[len(entries)-1]
	if first.codec != packCodecDict || last.codec != packCodecDict || int(last.logical) != int(first.logical)+len(packDictCopyMagic) {
		t.Fatalf("dictionary records missing: %+v %+v", first, last)
	}
	b5Flip(t, path, int64(first.off)+100)
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := s.GetObject("b", "a")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read with the first dictionary copy damaged: %v", err)
	}
	s.Close()
	packDicts.Lock()
	clear(packDicts.m)
	packDicts.order = nil
	packDicts.Unlock()
	b5Flip(t, path, int64(last.off)+100)
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, got, err := s.GetObject("b", "a"); err == nil && !bytes.Equal(got, data) {
		t.Fatal("wrong bytes with both dictionary copies damaged")
	} else if err == nil {
		t.Fatal("read succeeded with both dictionary copies damaged")
	}
}

// TestB5FeatureGate: a store holding restartable records refuses a build without
// the feature, and a pack's records stay unreadable to readers that cannot resolve
// dictionaries (bundles use decodePackPayload).
func TestB5FeatureGate(t *testing.T) {
	dir, _ := b5Blocked(t)
	var f storeFormat
	b, _ := os.ReadFile(filepath.Join(dir, "FORMAT.json"))
	if err := json.Unmarshal(b, &f); err != nil || !slices.Contains(f.Features, packBlockedFeature) {
		t.Fatalf("FORMAT.json: %s", b)
	}
	f.Features = slices.DeleteFunc(f.Features, func(x string) bool { return x == packBlockedFeature })
	f.Features = append(f.Features, "packblk2")
	if f.unknownFeature() != "packblk2" {
		t.Fatal("unknown feature not reported")
	}
	path, entries := b5OnlyPack(t, dir)
	pf, _ := os.Open(path)
	defer pf.Close()
	for _, e := range entries {
		if e.codec != packCodecBlocked {
			continue
		}
		buf := make([]byte, e.stored)
		pf.ReadAt(buf, int64(e.off))
		if _, err := decodePackPayload(e.codec, buf, e.logical, e.sha, nil); err == nil {
			t.Fatal("a blocked record decoded without its dictionary")
		}
		break
	}
}

// TestB5EncodeBlockedRule: blocked payloads obey the compressor rule and decode exactly.
func TestB5EncodeBlockedRule(t *testing.T) {
	dict := trainPackDict([][]byte{b5Text(1, 64<<10), b5Text(2, 64<<10)})
	d := &packDict{sha: sha256.Sum256(dict), data: dict}
	c := newBlockCompressor(true, 15)
	c.blk = d
	get := func(sha [32]byte) ([]byte, error) { return dict, nil }
	for _, in := range [][]byte{b5Text(5, 200<<10), b5Text(6, 32<<10+1), append(b5Text(7, 64<<10), genRandomBytes(1, 64<<10)...), genRandomBytes(2, 100<<10)} {
		payload, codec := c.encode(in)
		sum := sha256.Sum256(in)
		got, err := decodePackPayloadD(codec, slices.Clone(payload), uint32(len(in)), sum, nil, get, true)
		if err != nil || !bytes.Equal(got, in) {
			t.Fatalf("codec %d: %v", codec, err)
		}
		if codec == packCodecBlocked && len(payload) >= len(in)-len(in)/16 {
			t.Fatalf("blocked payload of %d for %d logical", len(payload), len(in))
		}
	}
	if _, codec := c.encode(genRandomBytes(3, 200<<10)); codec != packCodecRaw {
		t.Fatalf("incompressible input stored with codec %d", codec)
	}
	_ = hex.EncodeToString
	_ = strings.Repeat
}

// BenchmarkB5DecodeWhole compares whole-record decode of one 192 KiB text chunk: DEFLATE vs restartable.
func BenchmarkB5DecodeWhole(b *testing.B) {
	data := b5Text(11, 192<<10)
	sum := sha256.Sum256(data)
	dict := trainPackDict([][]byte{b5Text(12, 128<<10), b5Text(13, 128<<10)})
	for _, mode := range []string{"deflate", "blocked32"} {
		c := newPackCompressor()
		if mode == "blocked32" {
			c.blockLog, c.blk = 15, &packDict{sha: sha256.Sum256(dict), data: dict}
		}
		payload, codec := c.encode(data)
		payload = slices.Clone(payload)
		get := func([32]byte) ([]byte, error) { return dict, nil }
		dst := make([]byte, len(data))
		b.Run(mode, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := decodePackPayloadD(codec, payload, uint32(len(data)), sum, dst, get, false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// b5Family builds three 2 MiB text revisions, migrates them to windowed deltas (archive
// order, depth 2) and reclaims the CDC copies: one authority family with a pin base.
func b5Family(t *testing.T) *authLab {
	defer func(a, b int64) { authWindowMin, authWindowSize = a, b }(authWindowMin, authWindowSize)
	authWindowMin, authWindowSize = 1<<20, 256<<10
	l := newAuthLab(t)
	v := b5Text(21, 2<<20)
	for i := range 3 {
		l.put("k/v"+string(rune('0'+i)), v)
		v = b5Mutate(v, uint64(i), 40)
	}
	l.reclaim()
	if rep := l.migrate(AuthPolicy{MaxDepth: 2, MaxBases: 1, MinSaving: 0.05, AllowDelta: true}); rep.Admitted < 2 {
		t.Fatalf("admitted %d", rep.Admitted)
	}
	l.reclaim()
	l.exact("migrated")
	return l
}

func b5Live(t *testing.T, s *Store) *liveSet {
	t.Helper()
	rr, err := s.computeReachability(false)
	if err != nil || !rr.OK() {
		t.Fatalf("reachability: %v %v", err, rr.Issues)
	}
	return rr.liveSet()
}

// TestB5ReconRelayout: the plan is deterministic and covers the family; the carried
// packs hold its records in plan order; reads stay exact; the relaid family is not
// planned again; restartable records compose with it.
func TestB5ReconRelayout(t *testing.T) {
	l := b5Family(t)
	s := l.s
	live := b5Live(t, s)
	p1, err := s.planRecon(live, true)
	mustOK(t, err)
	p2, err := s.planRecon(live, true)
	mustOK(t, err)
	if p1.Families != 1 || p1.Probes == 0 || p1.Records == 0 || !maps.Equal(p1.rank, p2.rank) {
		t.Fatalf("plan: %+v / %+v", p1, p2)
	}
	l.close()
	defer func(g float64) { reconMinGain = g }(reconMinGain)
	reconMinGain = -1 // relayout whatever the estimate
	res := b5Repack(t, l.dir, repackOptions{Layout: layoutRecon, BlockLog: 15})
	if res.Recon == nil || res.Recon.Planned != 1 || res.PacksWritten == 0 {
		t.Fatalf("relayout: %+v", res)
	}
	l.open()
	l.exact("relaid")
	l.verifyOK("relaid")
	// Planned records sit in plan order across the new packs.
	st := l.s.packSnap()
	type at struct {
		rank int
		pos  [2]int64
	}
	var got []at
	for sha, r := range p1.rank {
		loc, ok := st.lookup(sha)
		if !ok {
			t.Fatalf("planned record %x missing", sha[:4])
		}
		got = append(got, at{r, [2]int64{int64(loc.pack), int64(loc.off)}})
	}
	slices.SortFunc(got, func(a, b at) int { return a.rank - b.rank })
	order := map[int32]string{}
	for i, p := range st.packs {
		order[int32(i)] = p.id
	}
	for i := 1; i < len(got); i++ {
		a, b := got[i-1].pos, got[i].pos
		if a[0] == b[0] && a[1] > b[1] {
			t.Fatalf("rank %d placed before rank %d", got[i].rank, got[i-1].rank)
		}
	}
	reconMinGain = 0.15
	p3, err := l.s.planRecon(b5Live(t, l.s), false)
	mustOK(t, err)
	if p3.Planned != 0 || p3.WindowsNow > 0 && 1-p3.WindowsPlanned/p3.WindowsNow >= reconMinGain {
		t.Fatalf("relaid family planned again: %+v", p3)
	}
	if blocked, _ := b5BlockedLocs(l.s); len(blocked) == 0 {
		t.Fatal("relayout with -record-blocks wrote no restartable record")
	}
	l.close()
	l.open()
	l.exact("reopened")
}

// TestB5OnlineRelayoutUnderLoad runs online passes with the recon layout and
// restartable records while readers and writers work; every read is exact.
func TestB5OnlineRelayoutUnderLoad(t *testing.T) {
	l := b5Family(t)
	l.close()
	s := b4OpenMaint(t, l.dir)
	l.s = s
	defer func(g float64) { reconMinGain = g }(reconMinGain)
	reconMinGain = -1
	opt := b4Opts()
	opt.Layout, opt.BlockLog = layoutRecon, 15
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	errs := []string{}
	fail := func(f string, a ...any) { mu.Lock(); errs = append(errs, fmt.Sprintf(f, a...)); mu.Unlock() }
	for g := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(g), 3))
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				k := "k/v" + string(rune('0'+r.IntN(3)))
				want := l.want[k]
				off := r.Int64N(int64(len(want)) - 4096)
				if _, _, got, err := s.GetObjectRange("b", k, byteRange{off, off + 4095}); err != nil || !bytes.Equal(got, want[off:off+4096]) {
					fail("%s@%d: %v", k, off, err)
				}
			}
		}()
	}
	extra := map[string][]byte{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			k := fmt.Sprintf("w%02d", i)
			body := b5Text(uint64(100+i), 50<<10)
			if _, err := s.PutObject("b", k, body, "text/plain", nil); err != nil {
				fail("put %s: %v", k, err)
				return
			}
			mu.Lock()
			extra[k] = body
			mu.Unlock()
		}
	}()
	var relaid bool
	for range 3 {
		rep, err := s.maintainOnline(context.Background(), opt)
		mustOK(t, err)
		if rep.Recon != nil && rep.Recon.Planned > 0 {
			relaid = true
		}
		l.exact("after pass")
	}
	close(stop)
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("%d bad reads, first: %s", len(errs), errs[0])
	}
	if !relaid {
		t.Fatal("no pass relaid the family")
	}
	maps.Copy(l.want, extra)
	reconMinGain = 0.15
	rep, err := s.maintainOnline(context.Background(), opt)
	mustOK(t, err)
	if rep.Recon != nil && rep.Recon.Planned > 0 {
		t.Fatalf("a relaid family was relaid again: %+v", rep.Recon)
	}
	l.exact("converged")
	l.verifyOK("online")
	l.close()
	l.open()
	l.exact("reopened")
	l.verifyOK("reopened")
}

// TestB5HelperProcess is the child of the crash cuts: one online pass with the
// recon layout and restartable records, exiting at the Nth firing of a hook.
func TestB5HelperProcess(t *testing.T) {
	dir := os.Getenv("ZS3_B5_DIR")
	if dir == "" {
		return
	}
	point := os.Getenv("ZS3_B5_POINT")
	var n int
	testHook = func(p string) {
		if p == point {
			if n++; n == 1 {
				os.Exit(95)
			}
		}
	}
	reconMinGain = -1
	s := b4OpenMaint(t, dir)
	opt := b4Opts()
	opt.Layout, opt.BlockLog = layoutRecon, 15
	_, err := s.maintainOnline(context.Background(), opt)
	mustOK(t, err)
	os.Exit(0)
}

// TestB5CrashCutsRecover: an exit at any publication or retirement boundary of a
// relayout pass leaves a store that reopens, deep-verifies and reads exactly, and
// the next passes converge.
func TestB5CrashCutsRecover(t *testing.T) {
	for _, point := range []string{hookMaintMarked, hookPackValidated, hookPackAfterRename, hookPackAfterDirSync,
		hookMaintCarried, hookMaintSwapped, hookMaintDrained, hookMaintBeforePackUnlink, hookMaintRetired} {
		t.Run(point, func(t *testing.T) {
			l := b5Family(t)
			mustOK(t, l.s.DeleteObject("b", "k/v0")) // dead data too, so the pass also retires
			delete(l.want, "k/v0")
			b4Prune(t, l.s)
			l.close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestB5HelperProcess$", "-test.timeout=300s")
			cmd.Env = append(os.Environ(), "ZS3_B5_DIR="+l.dir, "ZS3_B5_POINT="+point)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 95 {
				t.Fatalf("cut not reached: %v\n%s", err, out)
			}
			l.open()
			l.exact("after the cut")
			l.verifyOK("after the cut")
			l.close()
			s := b4OpenMaint(t, l.dir)
			opt := b4Opts()
			opt.Layout, opt.BlockLog = layoutRecon, 15
			_, err = s.maintainOnline(context.Background(), opt)
			mustOK(t, err)
			rep, err := s.maintainOnline(context.Background(), opt)
			mustOK(t, err)
			if rep.PacksRetired != 0 || rep.PendingPacks != 0 || rep.Recon != nil && rep.Recon.Planned > 0 {
				t.Fatalf("passes after the cut do not converge: %+v", rep)
			}
			mustOK(t, s.Close())
			l.open()
			l.exact("converged")
			l.verifyOK("converged")
		})
	}
}

// TestB5DamagedStagedPackRefused: publication proves every block digest, so a
// staged pack whose table or block was damaged is removed, not published.
func TestB5DamagedStagedPackRefused(t *testing.T) {
	for _, where := range []string{"table", "block", "dictionary"} {
		t.Run(where, func(t *testing.T) {
			s := newTestStore(t)
			var batch []compactCandidate
			data := map[[32]byte][]byte{}
			for i := range 6 {
				d := b5Text(uint64(40+i), 150<<10)
				sum := sha256.Sum256(d)
				data[sum] = d
				batch = append(batch, compactCandidate{sum: sum, size: int64(len(d))})
			}
			read := func(sum [32]byte) ([]byte, error) { return data[sum], nil }
			path, entries, err := s.stagePack(tierHot, batch, read, func(compactCandidate, error) error { return nil }, newBlockCompressor(true, 15))
			mustOK(t, err)
			var off int64
			for _, e := range entries {
				switch {
				case where == "dictionary" && e.codec == packCodecDict:
					off = int64(e.off) + 10
				case where == "table" && e.codec == packCodecBlocked:
					off = int64(e.off) + packBlockHeader + 4 + 1
				case where == "block" && e.codec == packCodecBlocked:
					off = int64(e.off) + int64(e.stored) - 5
				}
				if off != 0 {
					break
				}
			}
			if off == 0 {
				t.Fatalf("no %s record staged", where)
			}
			b5Flip(t, path, off)
			if _, err := s.publishPack(tierHot, path, entries); err == nil {
				t.Fatal("damaged staged pack was published")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("damaged staging file left behind")
			}
			if ps, _ := filepath.Glob(filepath.Join(s.root, "packs", "*.pack")); len(ps) != 0 {
				t.Fatalf("published: %v", ps)
			}
		})
	}
}

// TestB5RevertAllKeepsPackFeature: reverting every authority record clears the
// authority features but not packblk, which describes pack records that remain.
func TestB5RevertAllKeepsPackFeature(t *testing.T) {
	l := b5Family(t)
	l.close()
	b5Repack(t, l.dir, repackOptions{BlockLog: 15})
	l.open()
	if _, err := l.s.AuthorityRevertAll(); err != nil {
		t.Fatal(err)
	}
	f := l.s.formatSnapshot()
	if !slices.Contains(f.Features, packBlockedFeature) || slices.Contains(f.Features, authZW2Feature) {
		t.Fatalf("features after revert -all: %v", f.Features)
	}
	l.exact("reverted")
	l.close()
	l.open()
	l.exact("reopened")
	l.verifyOK("reverted")
}

// TestB5RepeatedSpanAdmits: the first small range of a restartable chunk reads
// only blocks; a repeat reads it whole so the chunk cache admits it; the next
// repeat is a cache hit with no pack read (served CDC and authority paths alike).
func TestB5RepeatedSpanAdmits(t *testing.T) {
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "32")
	dir, data := b5Blocked(t)
	s, err := OpenStore(dir)
	mustOK(t, err)
	defer s.Close()
	var spans, partial int
	var mu sync.Mutex
	tr := &packTrace{OnRecord: func(_ *[32]byte, _ int32, _ int64, stored, logical uint32, codec byte, _, _ int64) {
		mu.Lock()
		spans++
		if codec == packCodecBlocked && logical <= 64<<10 {
			partial++
		}
		mu.Unlock()
	}}
	s.packTrace = tr
	read := func() (records, runs int) {
		mu.Lock()
		spans, partial = 0, 0
		mu.Unlock()
		r0 := tr.ReadAts.Load()
		off := int64(700_000)
		_, _, got, err := s.GetObjectRange("b", "a", byteRange{off, off + 4095})
		if err != nil || !bytes.Equal(got, data[off:off+4096]) {
			t.Fatalf("range: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		return spans, int(tr.ReadAts.Load() - r0)
	}
	s.purgeReadCaches()
	if n, _ := read(); n == 0 || partial == 0 {
		t.Fatalf("first read: %d records, %d partial", n, partial)
	}
	read() // read whole: admitted
	if n, runs := read(); n != 0 || runs != 0 {
		t.Fatalf("third read still read the pack: %d records, %d runs", n, runs)
	}
}

func TestB5RepeatedSpanAdmitsAuthority(t *testing.T) {
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "32")
	l := b5Family(t)
	l.close()
	b5Repack(t, l.dir, repackOptions{BlockLog: 15})
	l.open()
	var n int
	var mu sync.Mutex
	l.s.packTrace = &packTrace{OnRecord: func(*[32]byte, int32, int64, uint32, uint32, byte, int64, int64) { mu.Lock(); n++; mu.Unlock() }}
	l.s.purgeReadCaches()
	want := l.want["k/v0"] // the deepest delta in archive order
	for i := range 3 {
		mu.Lock()
		n = 0
		mu.Unlock()
		_, _, got, err := l.s.GetObjectRange("b", "k/v0", byteRange{900_000, 900_000 + 4095})
		if err != nil || !bytes.Equal(got, want[900_000:900_000+4096]) {
			t.Fatalf("read %d: %v", i, err)
		}
		mu.Lock()
		if i == 2 && n != 0 {
			t.Fatalf("third repeat read %d pack records", n)
		}
		mu.Unlock()
	}
}
