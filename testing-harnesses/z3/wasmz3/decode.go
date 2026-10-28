package wasmz3

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"zeros3-z3/idxref"
)

func idxErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrFormat, err)
}

func putSlot(buf []byte, s *Slot, v uint32) error {
	b := buf[s.Off : s.Off+int64(s.W)]
	ok := false
	if s.Signed {
		ok = putSLEB33(b, int(s.W), uint64(v))
	} else {
		ok = putLEB(b, int(s.W), uint64(v))
	}
	if !ok {
		return bad("value %d does not fit the %d-byte LEB128 at %d", v, s.W, s.Off)
	}
	return nil
}

// Decode rebuilds the module from Meta and the component bytes (in order). Every length it
// allocates from is bounded by the component bytes it was handed.
func Decode(meta []byte, comps [][]byte) ([]byte, error) {
	if len(meta) < len(repMagic)+4 || string(meta[:4]) != repMagic {
		return nil, bad("representation header")
	}
	cfg := Config{K0: int(meta[4]), Auto: int(meta[5]), UseNames: meta[6]&1 != 0}
	if cfg.K0 < 1 || cfg.K0 > 4 || cfg.Auto > 100 {
		return nil, bad("K0 %d auto %d", cfg.K0, cfg.Auto)
	}
	size, n := binary.Uvarint(meta[7:])
	if n <= 0 || size < HeaderSize || size > 1<<31 || len(meta) != 7+n {
		return nil, bad("representation header")
	}
	var payload []byte
	var streams [NumStreams][]byte
	for _, c := range comps {
		pl, ss, err := idxref.SplitComp(c, NumStreams)
		if err != nil {
			return nil, idxErr(err)
		}
		payload = append(payload, pl...)
		for k := range ss {
			streams[k] = append(streams[k], ss[k]...)
		}
	}
	if int64(len(payload)) != int64(size)-HeaderSize {
		return nil, bad("payload is %d bytes, the header describes %d", len(payload), int64(size)-HeaderSize)
	}
	buf := append([]byte("\x00asm\x01\x00\x00\x00"), payload...)
	dir, err := Parse(bytes.NewReader(buf), int64(len(buf)), Limits{})
	if err != nil {
		return nil, err
	}
	m, err := Analyze(dir, buf, Limits{})
	if err != nil {
		return nil, err
	}
	c := &ctx{m: m, d: buf, id: maskedCopy(m, buf), cfg: cfg}
	c.buildTypes()
	restore := func(sp Space) error {
		ix, err := c.index(sp)
		if err != nil {
			return err
		}
		st := &streams[spIdx(sp)]
		for i := range c.m.Slots {
			s := &c.m.Slots[i]
			if s.Sp != sp {
				continue
			}
			h, rest, err := ix.Get(*st)
			if err != nil {
				return idxErr(err)
			}
			*st = rest
			if err := putSlot(buf, s, uint32(h)); err != nil {
				return err
			}
		}
		return nil
	}
	if err := restore(SpType); err != nil {
		return nil, err
	}
	// re-read the module-level tables that hold type indexes (imports, function section, tags)
	if c.m, err = Analyze(dir, buf, Limits{}); err != nil {
		return nil, err
	}
	if err := c.buildDefs(); err != nil {
		return nil, err
	}
	for _, sp := range []Space{SpFunc, SpGlobal, SpTable, SpMem, SpTag, SpData} {
		if err := restore(sp); err != nil {
			return nil, err
		}
	}
	if err := c.buildElems(); err != nil {
		return nil, err
	}
	if err := restore(SpElem); err != nil {
		return nil, err
	}
	for k := range streams {
		if len(streams[k]) != 0 {
			return nil, bad("%d unread bytes in the %s stream", len(streams[k]), SpaceName[k+1])
		}
	}
	return buf, nil
}
