package composez3

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"zeros3-z3/binref"
	"zeros3-z3/elfz3"
	"zeros3-z3/gopcln"
	"zeros3-z3/lab"
	"zeros3-z3/pez3"
)

// The executable inner transform: the Z3-12/13 reference normalization with the
// `full` slot set, symbol identities and automatic unit naming. Every binref stream
// is cut with production CDC into SHA-256 components; the inner recipe is a stream
// directory (kind, region, and the chunk digests and lengths; streams of up to
// inlineMax bytes live in the directory itself).
//
//	"EXR1" | n | n × [ kind (1 B) | region | flag (0 chunked, 1 inline) | length | chunks: count, count × (digest 32 B, length) | inline bytes ]

const (
	execInlineMax = 512
	execMaxStream = 1 << 30
)

var streamKinds = []string{"regions", "units", "payload", "refs"}

func kindCode(k string) (byte, error) {
	for i, s := range streamKinds {
		if s == k {
			return byte(i), nil
		}
	}
	return 0, fmt.Errorf("composez3: unknown binref stream %q", k)
}

func parseExec(data []byte) (*binref.Image, *gopcln.Table, []binref.Unit, error) {
	var im *binref.Image
	var err error
	var pe *pez3.Info
	switch {
	case bytes.HasPrefix(data, []byte("\x7fELF")):
		im, _, err = elfz3.Parse(data)
	case len(data) > 2 && data[0] == 'M' && data[1] == 'Z':
		im, pe, err = pez3.Parse(data)
	default:
		err = errors.New("not an ELF/PE executable")
	}
	if err != nil {
		return nil, nil, nil, err
	}
	pcln, _ := gopcln.Find(im)
	var units []binref.Unit
	switch {
	case len(im.Syms) > 0:
		units = im.UnitsFromSyms(false)
	case pcln != nil:
		units = im.CleanUnits(pcln.Units)
	case pe != nil && len(pe.Funcs) > 0:
		units = im.CleanUnits(pe.Funcs)
	default:
		units = im.UnitsFromBranches(binref.ScanX86(im))
	}
	return im, pcln, units, nil
}

func encodeExec(payload []byte) (*Inner, error) {
	im, pcln, units, err := parseExec(payload)
	if err != nil {
		return nil, err
	}
	var slots []binref.Slot
	for _, sl := range im.Slots {
		if sl.Class == binref.ClassReloc || sl.Class == binref.ClassTable {
			slots = append(slots, sl)
		}
	}
	if pcln != nil {
		slots = append(slots, pcln.Slots...)
	}
	slots = append(slots, binref.ScanX86(im)...)
	slots = append(slots, binref.ScanPtr(im)...)
	n, err := binref.Normalize(im, slots, units, binref.IdentSymbol)
	if err != nil {
		return nil, err
	}
	in := &Inner{Kind: KindExec, Detail: map[string]any{"streams": len(n.Streams)}}
	rec := append([]byte("EXR1"), binary.AppendUvarint(nil, uint64(len(n.Streams)))...)
	seen := map[lab.ID]bool{}
	for _, s := range n.Streams {
		kc, err := kindCode(s.Kind)
		if err != nil {
			return nil, err
		}
		rec = append(rec, kc)
		rec = binary.AppendUvarint(rec, uint64(s.Region))
		if len(s.Data) <= execInlineMax {
			rec = append(rec, 1)
			rec = binary.AppendUvarint(rec, uint64(len(s.Data)))
			rec = append(rec, s.Data...)
			continue
		}
		rec = append(rec, 0)
		rec = binary.AppendUvarint(rec, uint64(len(s.Data)))
		class := ClassMeta
		if s.Kind == "payload" {
			class = ClassPayload
		}
		var chunks [][]byte
		if err := lab.ChunkCDC(bytes.NewReader(s.Data), func(_ int64, c []byte) error {
			chunks = append(chunks, append([]byte(nil), c...))
			return nil
		}); err != nil {
			return nil, err
		}
		rec = binary.AppendUvarint(rec, uint64(len(chunks)))
		for _, c := range chunks {
			id := lab.ID(sha256.Sum256(c))
			rec = append(rec, id[:]...)
			rec = binary.AppendUvarint(rec, uint64(len(c)))
			if !seen[id] {
				seen[id] = true
				cc := c
				in.Comps = append(in.Comps, &lab.Comp{ID: id, Size: int64(len(c)), Class: class, Data: bytesData(cc)})
			}
		}
	}
	in.Recipe = rec
	return in, nil
}

func rebuildExec(w io.Writer, recipe []byte, get Getter) error {
	if len(recipe) < 5 || string(recipe[:4]) != "EXR1" {
		return ErrCorrupt
	}
	pos := 4
	uv := func() (uint64, error) {
		v, n := binary.Uvarint(recipe[pos:])
		if n <= 0 {
			return 0, ErrCorrupt
		}
		pos += n
		return v, nil
	}
	ns, err := uv()
	if err != nil || ns > uint64(len(recipe)) {
		return ErrCorrupt
	}
	var streams []binref.Stream
	var total uint64
	for i := uint64(0); i < ns; i++ {
		if pos >= len(recipe) {
			return ErrCorrupt
		}
		kc := recipe[pos]
		pos++
		if int(kc) >= len(streamKinds) {
			return ErrCorrupt
		}
		region, err := uv()
		if err != nil || region > 1<<20 {
			return ErrCorrupt
		}
		if pos >= len(recipe) {
			return ErrCorrupt
		}
		flag := recipe[pos]
		pos++
		ln, err := uv()
		if err != nil || ln > execMaxStream {
			return ErrCorrupt
		}
		total += ln
		if total > execMaxStream {
			return ErrCorrupt
		}
		st := binref.Stream{Kind: streamKinds[kc], Region: int(region)}
		if flag == 1 {
			if uint64(len(recipe)-pos) < ln {
				return ErrCorrupt
			}
			st.Data = append([]byte(nil), recipe[pos:pos+int(ln)]...)
			pos += int(ln)
		} else {
			nc, err := uv()
			if err != nil || nc > uint64(len(recipe)-pos)/33 {
				return ErrCorrupt
			}
			data := make([]byte, 0, min(ln, 1<<20))
			for j := uint64(0); j < nc; j++ {
				if len(recipe)-pos < 33 {
					return ErrCorrupt
				}
				var id lab.ID
				copy(id[:], recipe[pos:])
				pos += 32
				cl, err := uv()
				if err != nil {
					return err
				}
				b, err := get(id)
				if err != nil {
					return err
				}
				if uint64(len(b)) != cl || lab.ID(sha256.Sum256(b)) != id || uint64(len(data))+cl > ln {
					return fmt.Errorf("%w: chunk %x", ErrCorrupt, id[:6])
				}
				data = append(data, b...)
			}
			if uint64(len(data)) != ln {
				return ErrCorrupt
			}
			st.Data = data
		}
		streams = append(streams, st)
	}
	if pos != len(recipe) {
		return ErrCorrupt
	}
	out, err := binref.Reconstruct(streams)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}
