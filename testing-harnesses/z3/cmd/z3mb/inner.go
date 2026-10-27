package main

import (
	"bytes"
	"fmt"
	"sync/atomic"

	"zeros3-z3/dexz3"
	"zeros3-z3/jvmz3"
	"zeros3-z3/lab"
)

// Inner transforms for ZIP members (lab.Inner): the plaintext of a DEFLATE member, or the data of a
// stored member, that is a DEX or a class file becomes indexed-reference components; anything else
// (or anything the parser refuses) is left to CDC.

type innerStats struct{ ok, fallback, okBytes, fbBytes atomic.Int64 }

type dexInner struct {
	cfg dexz3.Config
	st  *innerStats
}

func (d dexInner) Encode(plain []byte) ([]lab.Piece, bool, error) {
	if len(plain) < 112 || string(plain[:4]) != "dex\n" {
		return nil, false, nil
	}
	f, err := dexz3.Parse(bytes.NewReader(plain), int64(len(plain)), dexz3.Limits{})
	if err == nil {
		var rep *dexz3.Rep
		if rep, err = dexz3.Encode(f, plain, d.cfg); err == nil {
			d.st.ok.Add(1)
			d.st.okBytes.Add(int64(len(plain)))
			return packPieces(rep.Meta, rep.Comps, "dexn"), true, nil
		}
	}
	if !isFallback(err) {
		return nil, false, err
	}
	d.st.fallback.Add(1)
	d.st.fbBytes.Add(int64(len(plain)))
	return nil, false, nil
}

func (d dexInner) Decode(pieces []lab.Piece, get func(lab.ID) ([]byte, error)) ([]byte, error) {
	meta, cs, err := unpackPieces(pieces, get)
	if err != nil {
		return nil, err
	}
	return dexz3.Decode(meta, cs)
}

type jvmInner struct {
	cfg jvmz3.Config
	st  *innerStats
}

func (j jvmInner) Encode(plain []byte) ([]lab.Piece, bool, error) {
	if len(plain) < 24 || plain[0] != 0xCA || plain[1] != 0xFE || plain[2] != 0xBA || plain[3] != 0xBE {
		return nil, false, nil
	}
	c, _, err := jvmz3.Parse(bytes.NewReader(plain), int64(len(plain)), jvmz3.Limits{})
	if err == nil {
		var rep *jvmz3.Rep
		if rep, err = jvmz3.Encode(c, plain, j.cfg); err == nil {
			j.st.ok.Add(1)
			j.st.okBytes.Add(int64(len(plain)))
			return packPieces(rep.Meta, rep.Comps, "jvmn"), true, nil
		}
	}
	if !(errorsIs(err, jvmz3.ErrFormat) || errorsIs(err, jvmz3.ErrUnsupported) || errorsIs(err, jvmz3.ErrLimit)) {
		return nil, false, err
	}
	j.st.fallback.Add(1)
	j.st.fbBytes.Add(int64(len(plain)))
	return nil, false, nil
}

func (j jvmInner) Decode(pieces []lab.Piece, get func(lab.ID) ([]byte, error)) ([]byte, error) {
	meta, cs, err := unpackPieces(pieces, get)
	if err != nil {
		return nil, err
	}
	return jvmz3.Decode(meta, cs)
}

// multiInner dispatches on the member's magic.
type multiInner struct{ dex, jvm lab.Inner }

func (m multiInner) Encode(plain []byte) ([]lab.Piece, bool, error) {
	if len(plain) >= 4 {
		switch {
		case string(plain[:4]) == "dex\n" && m.dex != nil:
			return m.dex.Encode(plain)
		case plain[0] == 0xCA && plain[1] == 0xFE && plain[2] == 0xBA && plain[3] == 0xBE && m.jvm != nil:
			return m.jvm.Encode(plain)
		}
	}
	return nil, false, nil
}

func (m multiInner) Decode(pieces []lab.Piece, get func(lab.ID) ([]byte, error)) ([]byte, error) {
	// the meta record's magic names the codec
	if len(pieces) == 0 || pieces[0].Comp != nil {
		return nil, fmt.Errorf("missing meta piece")
	}
	switch string(pieces[0].Inline[:min(4, len(pieces[0].Inline))]) {
	case "DXN1":
		if m.dex != nil {
			return m.dex.Decode(pieces, get)
		}
	case "JVN1":
		if m.jvm != nil {
			return m.jvm.Decode(pieces, get)
		}
	}
	return nil, fmt.Errorf("unknown inner codec")
}
