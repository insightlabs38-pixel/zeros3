package main

// FB-01R fuzz targets, run locally for a bounded time, e.g.
//   go test -run '^$' -fuzz '^FuzzFB01RJudgeTail$' -fuzztime 60s .
//
//   - FuzzFB01RWatermarkSlot: the slot decoder on arbitrary bytes;
//   - FuzzFB01RJudgeTail: a slot at a frame end of a v1 journal, then one
//     flipped byte and one cut anywhere. Damage below the slot must refuse,
//     a discard never reaches below it, and a plain cut past it in the same
//     boot is always recovered;
//   - FuzzFB01RDesignBRecover: the same for the v2 prototype's recovery.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func FuzzFB01RWatermarkSlot(f *testing.F) {
	w := journalWatermark{Gen: 3, Flags: wmFlagSynced, Offset: 4096, FrameOff: 3900, Seq: 12, LastCRC: 7, PID: 9, Unix: 1}
	f.Add(w.encode())
	f.Add(make([]byte, journalWatermarkSlot))
	f.Add([]byte("ZSWM"))
	f.Fuzz(func(t *testing.T, b []byte) {
		got, ok := decodeJournalWatermark(b)
		if !ok {
			return
		}
		if got.Offset < 0 || got.FrameOff < -1 || got.FrameOff > got.Offset || (got.Offset == 0) != (got.Seq == 0) {
			t.Fatalf("decoder accepted an inconsistent slot: %+v", got)
		}
		again, ok := decodeJournalWatermark(got.encode())
		if !ok || *again != *got {
			t.Fatalf("slot does not round-trip: %+v -> %+v", got, again)
		}
	})
}

func FuzzFB01RJudgeTail(f *testing.F) {
	f.Add(uint8(6), uint8(3), uint16(0), byte(0), uint16(0), true, false)
	f.Add(uint8(6), uint8(5), uint16(0), byte(0), uint16(400), true, false)    // cut past the slot
	f.Add(uint8(6), uint8(2), uint16(40), byte(1), uint16(0), true, false)     // damage below it
	f.Add(uint8(6), uint8(2), uint16(0), byte(0), uint16(120), false, true)    // cut below it
	f.Add(uint8(8), uint8(7), uint16(500), byte(0x80), uint16(0), false, true) // damage past it, restart
	f.Fuzz(func(t *testing.T, n, p uint8, at uint16, xor byte, cut uint16, same, synced bool) {
		frames := 1 + int(n%12)
		img := fb01Encode(1, frames)
		// The slot sits at the end of frame k (k = 0: the empty journal).
		k := int(p) % (frames + 1)
		var w journalWatermark
		off := int64(0)
		for i := 0; i < k; i++ {
			w.FrameOff = off
			off += journalHeaderSize + int64(be32(img[off+16:off+20])) + 4
		}
		w.Offset, w.Seq, w.Gen = off, uint64(k), 2
		if k > 0 {
			w.LastCRC = be32(img[off-4 : off])
		}
		w.Boot = currentBootID()
		if !same {
			w.Boot[0] ^= 0xff
		}
		if synced {
			w.Flags |= wmFlagSynced
		}
		d := bytes.Clone(img)
		damagedBelow := false
		if xor != 0 {
			i := int(at) % len(d)
			d[i] ^= xor
			damagedBelow = int64(i) < w.Offset
		}
		if c := int(cut); c > 0 && c < len(d) {
			d = d[:c]
			damagedBelow = damagedBelow || int64(c) < w.Offset
		}
		path := filepath.Join(t.TempDir(), "j")
		if err := os.WriteFile(path, d, 0o644); err != nil {
			t.Fatal(err)
		}
		jf, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer jf.Close()
		sc, _, err := scanJournalRange(jf, 0, int64(len(d)), 0, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		v, err := judgeTail(jf, &w, sc, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if damagedBelow && v.Problem == nil {
			t.Fatalf("damage below the slot at %d was not refused: valid end %d, %+v", w.Offset, sc.ValidEnd, v)
		}
		if v.Discard && (v.Problem != nil || sc.Damage == nil || sc.Damage.Offset < w.Offset) {
			t.Fatalf("a discard reaches below the slot: %+v damage %+v", v, sc.Damage)
		}
		if same && xor == 0 && !damagedBelow && sc.Damage != nil && !v.Discard {
			t.Fatalf("a cut past the slot in the same boot was not recovered: %+v damage %+v", v, sc.Damage)
		}
		if !same && !synced && v.Discard {
			t.Fatalf("a lazy slot from another boot discarded a tail: %+v", v)
		}
	})
}

func FuzzFB01RDesignBRecover(f *testing.F) {
	base := v2Build(f, 7)
	last := int8(len(base.groups) - 1)
	f.Add(last, uint16(0), byte(0), uint16(0), true)
	f.Add(int8(2), uint16(0), byte(0), uint16(9000), false)
	f.Add(int8(1), uint16(300), byte(4), uint16(0), true)
	f.Add(int8(-1), uint16(0), byte(0), uint16(12000), false)
	f.Fuzz(func(t *testing.T, g int8, at uint16, xor byte, cut uint16, synced bool) {
		gi := int(g) % len(base.groups)
		if gi < -1 {
			gi = -1
		}
		img := base.withSeal(base.data, gi, synced)
		sealEnd := int64(v2HeaderSize)
		if gi >= 0 {
			sealEnd = base.groups[gi].end
		}
		body := int64(len(img) - v2HeaderSize)
		damagedBelow := false
		if xor != 0 {
			i := v2HeaderSize + int64(at)%body
			img[i] ^= xor
			damagedBelow = i < sealEnd
		}
		if c := v2HeaderSize + int64(cut); cut > 0 && c < int64(len(img)) {
			img = img[:c]
			damagedBelow = damagedBelow || c < sealEnd
		}
		out := v2Recover(img)
		switch out.verdict {
		case "ok", "recover":
			if damagedBelow || out.keep < sealEnd || !bytes.Equal(img[v2HeaderSize:sealEnd], base.data[v2HeaderSize:sealEnd]) {
				t.Fatalf("%s kept %d with the seal at %d (damage below: %v)", out.verdict, out.keep, sealEnd, damagedBelow)
			}
		case "refuse-below", "refuse-later":
		default:
			t.Fatalf("verdict %q for a well-formed seal", out.verdict)
		}
	})
}
