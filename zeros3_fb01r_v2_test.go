package main

// FB-01R Design B, a test-only prototype: a versioned journal file (v2) with
// an in-file seal and explicit group commit records. Nothing here is used by
// the engine.
//
// Layout: [0, 8 KiB) is the header, two 512-byte seal slots at offsets 0 and
// 4096: magic "ZSJ2", version, flags (bit 0: synced), generation, the sealed
// end S, its group number, the chained CRC at S, CRC32C. The body starts at
// 8 KiB and holds v1 frames; every group ends with a commit frame whose payload
// is the group number, the group's first offset, its frame count and the
// chained CRC crc32c(previous chain || the group's bytes).
//
// Writers:
//   - piggyback: after a group's fsync, write the seal for it (no fsync) and
//     acknowledge. The next group's fsync makes that seal durable (it covers
//     the whole file), so the durable seal lags the acknowledged end by at
//     most one group, at no extra fsync.
//   - sync: fsync the seal before the acknowledgement: one extra fsync.
//
// Recovery (no boot identity: B is judged on what is durable): the body must
// validate as whole groups through S with the sealed chain; damage past S is
// discarded as unacknowledged unless a later valid commit record shows the
// damaged group was followed by another (then it was durable, and so
// acknowledged: refuse). Within the crash model that loses nothing. The
// piggyback residual: damage to, or a frame-boundary cut of, the one
// acknowledged group the seal may lag behind cannot be told from an
// interrupted write after a power loss.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

const (
	v2HeaderSize = 8192
	v2SlotSize   = 512
	v2SlotStride = 4096
	v2Magic      = "ZSJ2"
	v2CommitType = byte(0xC0)
	v2CommitLen  = 8 + 8 + 4 + 4
)

type v2Seal struct {
	gen    uint64
	synced bool
	end    int64 // S: the end of the sealed group
	group  uint64
	chain  uint32
}

func (s v2Seal) encode() []byte {
	b := make([]byte, v2SlotSize)
	copy(b, v2Magic)
	binary.BigEndian.PutUint16(b[4:], 2)
	if s.synced {
		b[6] = 1
	}
	binary.BigEndian.PutUint64(b[8:], s.gen)
	binary.BigEndian.PutUint64(b[16:], uint64(s.end))
	binary.BigEndian.PutUint64(b[24:], s.group)
	binary.BigEndian.PutUint32(b[32:], s.chain)
	binary.BigEndian.PutUint32(b[v2SlotSize-4:], crc32.Checksum(b[:v2SlotSize-4], castagnoliTable))
	return b
}

func v2ReadSeal(image []byte) (v2Seal, bool) {
	var best v2Seal
	found := false
	for _, off := range []int{0, v2SlotStride} {
		if off+v2SlotSize > len(image) {
			continue
		}
		b := image[off : off+v2SlotSize]
		if string(b[:4]) != v2Magic || binary.BigEndian.Uint16(b[4:]) != 2 || crc32.Checksum(b[:v2SlotSize-4], castagnoliTable) != binary.BigEndian.Uint32(b[v2SlotSize-4:]) {
			continue
		}
		s := v2Seal{synced: b[6]&1 != 0, gen: binary.BigEndian.Uint64(b[8:]), end: int64(binary.BigEndian.Uint64(b[16:])),
			group: binary.BigEndian.Uint64(b[24:]), chain: binary.BigEndian.Uint32(b[32:])}
		if !found || s.gen > best.gen {
			best, found = s, true
		}
	}
	return best, found
}

func v2Frame(seq uint64, typ byte, payload []byte) []byte {
	b := make([]byte, journalHeaderSize, journalHeaderSize+len(payload)+4)
	copy(b, journalMagic)
	binary.BigEndian.PutUint16(b[4:], journalFrameVersion)
	b[6] = typ
	binary.BigEndian.PutUint64(b[8:], seq)
	binary.BigEndian.PutUint32(b[16:], uint32(len(payload)))
	b = append(b, payload...)
	return binary.BigEndian.AppendUint32(b, crc32.Checksum(b, castagnoliTable))
}

// v2Journal is the prototype writer.
type v2Journal struct {
	f      *os.File
	sync   bool
	end    int64
	seq    uint64
	group  uint64
	chain  uint32
	gen    uint64
	fsyncs int
	writes int
}

func v2Create(path string, syncSeal bool) (*v2Journal, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	j := &v2Journal{f: f, sync: syncSeal, end: v2HeaderSize}
	if err := f.Truncate(v2HeaderSize); err != nil {
		return nil, err
	}
	if err := j.seal(true); err != nil {
		return nil, err
	}
	return j, nil
}

func (j *v2Journal) seal(durable bool) error {
	j.gen++
	s := v2Seal{gen: j.gen, synced: j.sync, end: j.end, group: j.group, chain: j.chain}
	if _, err := j.f.WriteAt(s.encode(), int64(j.gen%2)*v2SlotStride); err != nil {
		return err
	}
	j.writes++
	if durable || j.sync {
		j.fsyncs++
		return j.f.Sync()
	}
	return nil
}

// groupBytes is one group: its frames and its commit record.
func (j *v2Journal) groupBytes(payloads [][]byte) []byte {
	var b []byte
	seq := j.seq
	for _, p := range payloads {
		seq++
		b = append(b, v2Frame(seq, recordTypePutObjectRoot, p)...)
	}
	chain := crc32.Update(j.chain, castagnoliTable, b)
	var c [v2CommitLen]byte
	binary.BigEndian.PutUint64(c[0:], j.group+1)
	binary.BigEndian.PutUint64(c[8:], uint64(j.end))
	binary.BigEndian.PutUint32(c[16:], uint32(len(payloads)))
	binary.BigEndian.PutUint32(c[20:], chain)
	seq++
	return append(b, v2Frame(seq, v2CommitType, c[:])...)
}

// append writes one group and returns once it is acknowledged.
func (j *v2Journal) append(payloads [][]byte) error {
	b := j.groupBytes(payloads)
	if _, err := j.f.WriteAt(b, j.end); err != nil {
		return err
	}
	j.writes++
	j.fsyncs++
	if err := j.f.Sync(); err != nil {
		return err
	}
	j.chain = crc32.Update(j.chain, castagnoliTable, b[:len(b)-journalHeaderSize-v2CommitLen-4])
	j.end += int64(len(b))
	j.seq += uint64(len(payloads)) + 1
	j.group++
	return j.seal(false)
}

func (j *v2Journal) close() error {
	if err := j.seal(true); err != nil {
		return err
	}
	return j.f.Close()
}

// v2Group is a validated group in a scan.
type v2Group struct {
	start, end int64
	number     uint64
	chain      uint32
}

// v2Scan validates whole groups from the body start and returns them and the
// offset where the next group fails to validate (len(image) when none does).
func v2Scan(image []byte) (groups []v2Group, damage int64) {
	off, seq, chain, number := int64(v2HeaderSize), uint64(0), uint32(0), uint64(0)
	start := off
	n := 0
	for {
		if off == int64(len(image)) && off == start {
			return groups, off
		}
		if off+journalHeaderSize > int64(len(image)) {
			return groups, start
		}
		h := image[off : off+journalHeaderSize]
		plen := int64(binary.BigEndian.Uint32(h[16:]))
		end := off + journalHeaderSize + plen + 4
		if string(h[:4]) != journalMagic || plen > maxJournalPayload || end > int64(len(image)) ||
			crc32.Checksum(image[off:end-4], castagnoliTable) != binary.BigEndian.Uint32(image[end-4:end]) ||
			binary.BigEndian.Uint64(h[8:]) != seq+1 {
			return groups, start
		}
		seq++
		if h[6] != v2CommitType {
			n++
			off = end
			continue
		}
		p := image[off+journalHeaderSize : end-4]
		want := crc32.Update(chain, castagnoliTable, image[start:off])
		if plen != v2CommitLen || binary.BigEndian.Uint64(p[0:]) != number+1 || int64(binary.BigEndian.Uint64(p[8:])) != start ||
			int(binary.BigEndian.Uint32(p[16:])) != n || binary.BigEndian.Uint32(p[20:]) != want {
			return groups, start
		}
		number++
		chain = want
		groups = append(groups, v2Group{start: start, end: end, number: number, chain: chain})
		off, start, n = end, end, 0
	}
}

// v2LaterCommit reports a valid commit record past off for a group after the
// next one: the group at off was followed by another, so it was durable.
func v2LaterCommit(image []byte, off int64, next uint64) bool {
	for i := off; i+journalHeaderSize+v2CommitLen+4 <= int64(len(image)); i++ {
		h := image[i : i+journalHeaderSize]
		if string(h[:4]) != journalMagic || h[6] != v2CommitType || binary.BigEndian.Uint32(h[16:]) != v2CommitLen {
			continue
		}
		end := i + journalHeaderSize + v2CommitLen + 4
		if crc32.Checksum(image[i:end-4], castagnoliTable) != binary.BigEndian.Uint32(image[end-4:end]) {
			continue
		}
		if g := binary.BigEndian.Uint64(image[i+journalHeaderSize:]); g > next {
			return true
		}
	}
	return false
}

type v2Outcome struct {
	verdict string // ok, recover, refuse-below, refuse-later, refuse-seal
	keep    int64
}

// v2Recover decides what an open of image does.
func v2Recover(image []byte) v2Outcome {
	s, ok := v2ReadSeal(image)
	if !ok {
		return v2Outcome{verdict: "refuse-seal"}
	}
	groups, damage := v2Scan(image)
	validEnd, number := int64(v2HeaderSize), uint64(0)
	if len(groups) > 0 {
		g := groups[len(groups)-1]
		validEnd, number = g.end, g.number
	}
	if s.end > validEnd {
		return v2Outcome{verdict: "refuse-below"}
	}
	sealed := s.end == v2HeaderSize && s.group == 0 && s.chain == 0
	for _, g := range groups {
		if g.end == s.end {
			sealed = g.number == s.group && g.chain == s.chain
		}
	}
	if !sealed {
		return v2Outcome{verdict: "refuse-below"}
	}
	if damage == int64(len(image)) {
		return v2Outcome{verdict: "ok", keep: damage}
	}
	if v2LaterCommit(image, damage, number+1) {
		return v2Outcome{verdict: "refuse-later"}
	}
	return v2Outcome{verdict: "recover", keep: damage}
}

// v2Base is a closed v2 journal with each group's range and the seals a
// writer leaves at each point.
type v2Base struct {
	data   []byte
	groups []v2Group
}

func v2Build(t testing.TB, seed uint64) *v2Base {
	t.Helper()
	r := mrand.New(mrand.NewPCG(seed, 0x2))
	path := filepath.Join(t.TempDir(), "v2.log")
	j, err := v2Create(path, false)
	if err != nil {
		t.Fatal(err)
	}
	for range 4 + r.IntN(8) {
		nf := 1 + r.IntN(8)
		if r.IntN(10) == 0 {
			nf = 10 + r.IntN(20)
		}
		var ps [][]byte
		for range nf {
			p := make([]byte, 300+r.IntN(700))
			for i := range p {
				p[i] = byte(r.IntN(256))
			}
			ps = append(ps, p)
		}
		if err := j.append(ps); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.close(); err != nil {
		t.Fatal(err)
	}
	b := &v2Base{}
	if b.data, err = os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
	b.groups, _ = v2Scan(b.data)
	if len(b.groups) < 4 || b.groups[len(b.groups)-1].end != int64(len(b.data)) {
		t.Fatalf("v2 base: %d groups", len(b.groups))
	}
	return b
}

// withSeal returns body with a header holding the seal for the end of group
// g (g = -1: the empty journal).
func (b *v2Base) withSeal(body []byte, g int, synced bool) []byte {
	s := v2Seal{gen: 9, synced: synced, end: v2HeaderSize}
	if g >= 0 {
		s.end, s.group, s.chain = b.groups[g].end, b.groups[g].number, b.groups[g].chain
	}
	d := bytes.Clone(body)
	clear(d[:v2HeaderSize])
	copy(d, s.encode())
	return d
}

// TestFB01RDesignBMatrix runs the X14 scenarios against the v2 prototype. A
// trial's seal is one a real writer can leave durable at that moment: in sync
// mode the seal of the last acknowledged group; in piggyback mode that one or
// the one before (the lag). Outcomes are tallied as for the engine.
func TestFB01RDesignBMatrix(t *testing.T) {
	trials := 40
	var bases []*v2Base
	for i := range 6 {
		bases = append(bases, v2Build(t, uint64(2000+i)))
	}
	crash := func(r *mrand.Rand, b *v2Base, g int, garbage, stale bool) []byte {
		a, e := b.groups[g].start, b.groups[g].end
		d := bytes.Clone(b.data[:a])
		var size int64
		switch r.IntN(4) {
		case 0:
			size = a
		case 1:
			size = e
		case 2:
			size = min(a+int64(r.IntN(int((e-a)/fb01Sector)+1))*fb01Sector, e)
		default:
			size = a + 1 + r.Int64N(e-a)
		}
		mode, p := r.IntN(3), []float64{0.1, 0.5, 0.9}[r.IntN(3)]
		cut := a + r.Int64N(e-a+1)
		for off := a; off < size; off += fb01Sector {
			end := min(off+fb01Sector, size)
			keep := mode == 2 || mode == 0 && off < cut || mode == 1 && r.Float64() < p
			switch {
			case keep:
				d = append(d, b.data[off:end]...)
			case garbage && r.IntN(3) == 0:
				gb := make([]byte, end-off)
				for i := range gb {
					gb[i] = byte(r.IntN(256))
				}
				d = append(d, gb...)
			case stale && a > v2HeaderSize+fb01Sector:
				src := v2HeaderSize + r.Int64N((a-v2HeaderSize)/fb01Sector)*fb01Sector
				d = append(d, b.data[src:src+end-off]...)
			default:
				d = append(d, make([]byte, end-off)...)
			}
		}
		return d
	}
	corrupt := func(r *mrand.Rand, d []byte, g v2Group) {
		switch r.IntN(3) {
		case 0:
			d[g.start+r.Int64N(g.end-g.start)] ^= 1 << r.IntN(8)
		case 1:
			s := (g.start + r.Int64N(g.end-g.start)) / fb01Sector * fb01Sector
			clear(d[s:min(s+fb01Sector, int64(len(d)))])
		default:
			s := (g.start + r.Int64N(g.end-g.start)) / fb01Sector * fb01Sector
			for i := s; i < min(s+fb01Sector, int64(len(d))); i++ {
				d[i] = byte(r.IntN(256))
			}
		}
	}
	type scen struct {
		name string
		// make returns the body, the last acknowledged group, and whether
		// acknowledged history was damaged.
		make func(r *mrand.Rand, b *v2Base) ([]byte, int, bool)
	}
	last := func(b *v2Base) int { return len(b.groups) - 1 }
	scens := []scen{
		{"S0 process kill: final group cut at any byte", func(r *mrand.Rand, b *v2Base) ([]byte, int, bool) {
			g := last(b)
			return bytes.Clone(b.data[:b.groups[g].start+r.Int64N(b.groups[g].end-b.groups[g].start+1)]), g - 1, false
		}},
		{"S1 crash: torn final group", func(r *mrand.Rand, b *v2Base) ([]byte, int, bool) {
			return crash(r, b, last(b), false, false), last(b) - 1, false
		}},
		{"S2 crash: torn final group, garbage sectors", func(r *mrand.Rand, b *v2Base) ([]byte, int, bool) {
			return crash(r, b, last(b), true, false), last(b) - 1, false
		}},
		{"S8 crash: torn final group, stale sectors", func(r *mrand.Rand, b *v2Base) ([]byte, int, bool) {
			return crash(r, b, last(b), false, true), last(b) - 1, false
		}},
		{"S3 media: acknowledged final group damaged", func(r *mrand.Rand, b *v2Base) ([]byte, int, bool) {
			d := bytes.Clone(b.data)
			corrupt(r, d, b.groups[last(b)])
			return d, last(b), true
		}},
		{"S4 media: acknowledged earlier group damaged", func(r *mrand.Rand, b *v2Base) ([]byte, int, bool) {
			d := bytes.Clone(b.data)
			corrupt(r, d, b.groups[r.IntN(last(b))])
			return d, last(b), true
		}},
		{"S5 double: crash in final group + damage to the previous", func(r *mrand.Rand, b *v2Base) ([]byte, int, bool) {
			d := crash(r, b, last(b), false, false)
			corrupt(r, d, b.groups[last(b)-1])
			return d, last(b) - 1, true
		}},
		{"S6 truncation at a group boundary below the acknowledged end", func(r *mrand.Rand, b *v2Base) ([]byte, int, bool) {
			return bytes.Clone(b.data[:b.groups[r.IntN(last(b))].end]), last(b), true
		}},
		{"S7 truncation mid-group below the acknowledged end", func(r *mrand.Rand, b *v2Base) ([]byte, int, bool) {
			g := b.groups[r.IntN(len(b.groups))]
			return bytes.Clone(b.data[:g.start+1+r.Int64N(g.end-g.start-1)]), last(b), true
		}},
	}
	for _, mode := range []string{"piggyback", "sync"} {
		for si, sc := range scens {
			r := mrand.New(mrand.NewPCG(41, uint64(si)))
			counts := map[string]int{}
			loss, window := 0, 0
			for trial := range trials {
				b := bases[trial%len(bases)]
				body, acked, damaged := sc.make(r, b)
				sealAt := acked
				if mode == "piggyback" && r.IntN(2) == 0 {
					sealAt-- // the seal of the last acknowledged group never reached the disk
				}
				img := b.withSeal(body, sealAt, mode == "sync")
				out := v2Recover(img)
				counts[out.verdict]++
				if out.verdict == "ok" || out.verdict == "recover" {
					// Every acknowledged group must be intact in what is kept.
					kept := img[:out.keep]
					groups, _ := v2Scan(kept)
					e := b.groups[acked].end
					if len(groups) < acked+1 || int64(len(kept)) < e || !bytes.Equal(kept[v2HeaderSize:e], b.data[v2HeaderSize:e]) {
						loss++
						if mode == "piggyback" && sealAt < acked {
							window++ // the documented one-group residual
						} else {
							t.Errorf("%s | %s | trial %d: acknowledged group lost outside the window (%s)", mode, sc.name, trial, out.verdict)
						}
					}
				}
				if !damaged && out.verdict != "ok" && out.verdict != "recover" {
					t.Errorf("%s | %s | trial %d: an interrupted write was refused: %s", mode, sc.name, trial, out.verdict)
				}
			}
			t.Logf("B-%-9s %-62s ok %2d recover %2d refuse-below %2d refuse-later %2d loss %2d (in the one-group window %d)",
				mode, sc.name, counts["ok"], counts["recover"], counts["refuse-below"], counts["refuse-later"], loss, window)
			fb01EmitJSON(t, map[string]any{"kind": "design-b-summary", "build": os.Getenv("FB01_BUILD"), "mode": mode, "scenario": sc.name,
				"trials": trials, "outcomes": counts, "loss": loss, "loss_in_window": window})
		}
	}
}

// TestFB01RDesignBWriter checks the prototype writer's own invariants: one
// fsync per group in piggyback mode, two in sync mode, and a clean reopen.
func TestFB01RDesignBWriter(t *testing.T) {
	for _, syncSeal := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "v2.log")
		j, err := v2Create(path, syncSeal)
		if err != nil {
			t.Fatal(err)
		}
		base := j.fsyncs
		for i := range 50 {
			if err := j.append([][]byte{[]byte(fmt.Sprint("frame ", i)), []byte("second")}); err != nil {
				t.Fatal(err)
			}
		}
		perGroup := float64(j.fsyncs-base) / 50
		if want := map[bool]float64{false: 1, true: 2}[syncSeal]; perGroup != want {
			t.Fatalf("sync=%t: %.1f fsyncs per group, want %.0f", syncSeal, perGroup, want)
		}
		if err := j.close(); err != nil {
			t.Fatal(err)
		}
		img, _ := os.ReadFile(path)
		if out := v2Recover(img); out.verdict != "ok" || out.keep != int64(len(img)) {
			t.Fatalf("sync=%t: clean reopen: %+v", syncSeal, out)
		}
		// Without the header the body alone proves nothing.
		clear(img[:v2HeaderSize])
		if out := v2Recover(img); out.verdict != "refuse-seal" {
			t.Fatalf("no seal: %+v", out)
		}
	}
}
