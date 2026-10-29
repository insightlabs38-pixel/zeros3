package dexz3

import (
	"fmt"
	"hash/adler32"
	"io"
	"zeros3-z3/idxref"
)

// StreamConfig keeps the existing identity/cutter parameters. Emit must copy or
// persist each component synchronously; no root may be published before full proof.
type StreamConfig struct {
	K0, Auto int
	TempDir  string
	Observe  func(string)
	Cut      func(io.ReaderAt, []Sec) ([]int, error)
	Emit     func([]byte) error
}

// EncodeAt stages reference bytes on disk and emits bounded components. It does
// not retain input bytes, masked copies, reference slices, or output components.
func EncodeAt(f *File, r io.ReaderAt, cfg StreamConfig) (*Rep, error) {
	data := &walkSource{r: r, size: f.Size, lo: -1}
	if cfg.Emit == nil {
		return nil, bad("missing component sink")
	}
	spool, err := idxref.NewRefSpool(cfg.TempDir, 256<<20)
	if err != nil {
		return nil, err
	}
	defer spool.Close()
	scratch := make([]byte, 64<<10)

	k0 := cfg.K0
	if k0 == 0 {
		k0 = 3
	}
	if k0 < 1 || k0 > 4 {
		return nil, fmt.Errorf("dexz3: K0 %d", k0)
	}
	if cfg.Auto < 0 || cfg.Auto > 100 || cfg.Auto > 0 && k0 != 3 {
		return nil, fmt.Errorf("%w: noncanonical automatic-prefix parameters", ErrUnsupported)
	}
	slotCount := 0
	w, err := WalkAtVisit(f, r, WalkOpts{Offsets: true, Debug: true}, func(Slot) error {
		slotCount++
		if slotCount > 4<<20 {
			return fmt.Errorf("%w: patch count", ErrLimit)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if cfg.Observe != nil {
		cfg.Observe("walk")
	}
	// identities from the original
	var tb Tables
	if tb.ID[SpString], err = identStringsAt(data, f, w); err != nil {
		return nil, err
	}
	if tb.ID[SpType], err = identTypesAt(data, f, tb.ID[SpString]); err != nil {
		return nil, err
	}
	if tb.ID[SpProto], err = identProtosAt(data, f, tb.ID[SpType]); err != nil {
		return nil, err
	}
	if tb.ID[SpField], err = identFieldsAt(data, f, tb.ID[SpType], tb.ID[SpString]); err != nil {
		return nil, err
	}
	if tb.ID[SpMethod], err = identMethodsAt(data, f, tb.ID[SpType], tb.ID[SpProto], tb.ID[SpString]); err != nil {
		return nil, err
	}
	for sp := SpString; sp <= SpMethod; sp++ {
		if err := checkUnique(sp, tb.ID[sp]); err != nil {
			return nil, err
		}
	}
	if cfg.Observe != nil {
		cfg.Observe("identities")
	}
	var pi [numSpaces]*idxref.PrefixIndex
	var st Stats
	for sp := SpString; sp <= SpMethod; sp++ {
		pi[sp] = idxref.NewPrefixIndex(tb.ID[sp], spaceK0(k0, cfg.Auto, len(tb.ID[sp])))
		st.Collisions[sp] = pi[sp].Collisions()
	}
	if cfg.Observe != nil {
		cfg.Observe("prefixes")
	}
	// masked payload and per-slot reference bytes
	mOff, mLen := f.mapSpan()
	patches := make([]idxref.Patch, slotCount)
	patchIndex := 0
	if _, err := walkAtVisit(f, r, WalkOpts{Offsets: true, Debug: true}, func(s Slot) error {
		if patchIndex >= len(patches) {
			return bad("source changed during analysis")
		}
		var b [5]byte
		local := s
		local.Off = 0
		MaskSlot(b[:s.Len], &local)
		patches[patchIndex] = idxref.Patch{Off: s.Off, Len: s.Len, Bytes: b}
		patchIndex++
		return nil
	}, false); err != nil {
		return nil, err
	}
	if patchIndex != len(patches) {
		return nil, bad("source changed during analysis")
	}
	masked, err := idxref.NewWindowView(r, f.Size, nil, patches)
	if err != nil {
		return nil, err
	}
	if cfg.Observe != nil {
		cfg.Observe("patches")
	}
	oc, err := newOffCoderAt(f, w, masked, k0)
	if err != nil {
		return nil, err
	}
	if cfg.Observe != nil {
		cfg.Observe("offsets")
	}
	payload, err := idxref.NewWindowView(r, f.Size, []idxref.Span{{Off: 0, Len: HeaderSize}, {Off: mOff, Len: mLen}}, patches)
	if err != nil {
		return nil, err
	}
	if _, err := walkAtVisit(f, r, WalkOpts{Offsets: true, Debug: true}, func(slot Slot) error {
		s := &slot
		pos := int(s.Off) - HeaderSize
		if s.Off >= mOff+mLen {
			pos -= int(mLen)
		} else if s.Off+int64(s.Len) > mOff {
			return bad("slot overlaps the map list")
		}
		st.Slots[s.Space]++
		var b []byte
		var stream int
		if s.Space == SpOffset {
			stream = streamOff
			if b, err = oc.put(s.Kind, int64(s.Val)); err != nil {
				return err
			}
		} else {
			stream = spaceOf(s.Space)
			if b, err = pi[s.Space].Put(nil, s.Val); err != nil {
				return err
			}
		}
		st.StreamLen[stream] += len(b)
		if err := spool.Append(int64(pos), uint8(stream), 0, b); err != nil {
			return err
		}
		return nil
	}, false); err != nil {
		return nil, err
	}
	if data.err != nil {
		return nil, data.err
	}
	if cfg.Observe != nil {
		cfg.Observe("references")
	}
	st.Payload = int(payload.Size())
	secs := f.Sections(w, true)
	if err := spool.Rewind(); err != nil {
		return nil, err
	}
	ref, readErr := spool.Next()
	if readErr != nil && readErr != io.EOF {
		return nil, readErr
	}
	for si := range secs {
		sc := &secs[si]
		sc.Hash = make([]uint64, len(sc.Items))
		for i, st := range sc.Items {
			end := sc.End
			if i+1 < len(sc.Items) {
				end = sc.Items[i+1]
			}
			h, err := idxref.FNVRange(payload, int64(st), int64(end), scratch)
			if err != nil {
				return nil, err
			}
			for readErr == nil && ref.Pos < int64(st) {
				ref, readErr = spool.Next()
			}
			for readErr == nil && ref.Pos < int64(end) {
				h = idxref.FNV64(h^uint64(ref.Stream), ref.Bytes)
				ref, readErr = spool.Next()
			}
			if readErr != nil && readErr != io.EOF {
				return nil, readErr
			}
			sc.Hash[i] = h
		}
	}
	if cfg.Observe != nil {
		cfg.Observe("hashes")
	}
	var cuts []int
	if cfg.Cut != nil {
		cuts, err = cfg.Cut(payload, secs)
		if err != nil {
			return nil, err
		}
	}
	if len(cuts) == 0 || cuts[len(cuts)-1] != int(payload.Size()) {
		cuts = append(cuts, int(payload.Size()))
	}
	// components
	rep := &Rep{Stats: st, Cuts: cuts, Secs: secs}
	if err := spool.Rewind(); err != nil {
		return nil, err
	}
	ref, readErr = spool.Next()
	if readErr != nil && readErr != io.EOF {
		return nil, readErr
	}
	start := 0
	for _, end := range cuts {
		if end < start || end > int(payload.Size()) {
			return nil, fmt.Errorf("dexz3: bad cut %d", end)
		}
		if end-start > 16<<20 {
			return nil, bad("component payload limit")
		}
		refBytes := 0
		var streams [numStream][]byte
		for readErr == nil && ref.Pos < int64(end) {
			if len(ref.Bytes) > (16<<20)-(end-start)-refBytes-128 {
				return nil, bad("component references limit")
			}
			refBytes += len(ref.Bytes)
			streams[ref.Stream] = append(streams[ref.Stream], ref.Bytes...)
			ref, readErr = spool.Next()
		}
		if readErr != nil && readErr != io.EOF {
			return nil, readErr
		}
		if end-start > 16<<20 {
			return nil, bad("component payload limit")
		}
		p := make([]byte, end-start)
		if _, err := payload.ReadAt(p, int64(start)); err != nil {
			return nil, err
		}
		component := idxref.PackComp(p, streams[:])
		if len(component) > 16<<20 {
			return nil, bad("component limit")
		}
		if err := cfg.Emit(component); err != nil {
			return nil, err
		}
		rep.Stats.Comps++
		start = end
	}

	if cfg.Observe != nil {
		cfg.Observe("emitted")
	}
	// meta; Adler is streamed independently, preserving the original flag.
	var flags byte
	ad := adler32.New()
	if _, err := io.CopyBuffer(ad, io.NewSectionReader(r, 12, f.Size-12), scratch); err != nil {
		return nil, err
	}
	if ad.Sum32() == f.Hdr.Checksum {
		flags |= flagAdler
	}
	kb := byte(k0)
	if cfg.Auto > 0 {
		kb = 0x80 | byte(cfg.Auto)
	}
	m := append([]byte(repMagic), flags, kb)
	m = append(m, f.Raw[:]...)
	mapb := make([]byte, mLen)
	if _, err := r.ReadAt(mapb, mOff); err != nil {
		return nil, err
	}
	m = append(m, mapb...)
	rep.Meta = m
	// Sink output is untrusted until the caller performs the independent full proof.
	return rep, nil
}
