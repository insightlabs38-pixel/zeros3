package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"zeros3-z3/lab"
	"zeros3-z3/sstz3"
)

// file is one named table (or other file) of a state.
type file struct {
	name string
	data []byte
}

func sid(b []byte) lab.ID { return lab.ID(sha256.Sum256(b)) }

// loadRev reads the table files of one revision directory (the `_other` sidecar
// directory, when present, is returned separately: MANIFEST/CURRENT/OPTIONS/WAL).
func loadRev(dir string) (tables, other []file, err error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if e.Name() == "_other" {
				oe, _ := os.ReadDir(p)
				for _, o := range oe {
					b, err := os.ReadFile(filepath.Join(p, o.Name()))
					if err != nil {
						return nil, nil, err
					}
					other = append(other, file{o.Name(), b})
				}
			}
			continue
		}
		if !strings.HasSuffix(e.Name(), ".ldb") && !strings.HasSuffix(e.Name(), ".sst") {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		tables = append(tables, file{e.Name(), b})
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].name < tables[j].name })
	sort.Slice(other, func(i, j int) bool { return other[i].name < other[j].name })
	return
}

// repDef plans one file. fallback is set when the representation could not express
// the file and the plan is ordinary CDC.
type repDef struct {
	name   string
	oracle bool
	plan   func(f file) (pl *lab.Plan, fallback bool, err error)
}

func cdcPlan(f file) (*lab.Plan, error) {
	for _, r := range lab.Reps() {
		if r.Name == "cdc" {
			return r.Analyze(&lab.Input{Family: "sst", Format: "sst", Revision: f.name, Src: lab.BytesSource(f.name, f.data), Opts: lab.DefaultOptions()})
		}
	}
	return nil, errors.New("no cdc baseline")
}

// concatPlan makes a plan from contiguous spans of the file; small spans (< 512 B) live
// in the recipe like in the lab's other structural representations.
func concatPlan(f file, cuts []int64, class string) *lab.Plan {
	p := &lab.Plan{Concat: true}
	var pos int64
	for _, c := range cuts {
		if c <= pos {
			continue
		}
		b := f.data[pos:c]
		if len(b) < 512 {
			p.Pieces = append(p.Pieces, lab.Piece{Inline: b, Len: int64(len(b))})
		} else {
			bb := b
			p.Pieces = append(p.Pieces, lab.Piece{Comp: &lab.Comp{ID: sid(b), Size: int64(len(b)), Class: class, Data: func() ([]byte, error) { return bb, nil }}, Len: int64(len(b))})
		}
		pos = c
	}
	p.Rebuild = lab.ConcatRebuild(p)
	return p
}

// nativeBlocks segments the file at its data-block boundaries (trailers included); the
// metadata tail is one segment. grouped merges runs of blocks at content-defined
// (digest-decided) boundaries, as in Z3-18.
func nativeBlocks(f file, grouped bool) (*lab.Plan, bool, error) {
	t, err := sstz3.Open(bytes.NewReader(f.data), int64(len(f.data)), sstz3.Limits{})
	if err != nil || t.Reject() != nil {
		p, err := cdcPlan(f)
		return p, true, err
	}
	var cuts []int64
	cnt := 0
	for _, h := range t.Blocks {
		end := int64(h.Off + h.Size + 5)
		if !grouped {
			cuts = append(cuts, end)
			continue
		}
		cnt++
		d := sha256.Sum256(f.data[h.Off:end])
		if (int(d[0])<<8|int(d[1]))%16 == 0 && cnt >= 4 || cnt >= 64 {
			cuts = append(cuts, end)
			cnt = 0
		}
	}
	if cnt > 0 && len(t.Blocks) > 0 {
		cuts = append(cuts, t.DataEnd)
	}
	cuts = append(cuts, int64(len(f.data)))
	return concatPlan(f, cuts, "seg"), false, nil
}

// sstPlan encodes the file with sstz3 and turns the result into a lab plan: one piece per
// component, the recipe head as plan metadata (the digests are the pieces), and a Rebuild
// that decodes from the serialized recipe bytes and the components only.
func sstPlan(f file, opt sstz3.Options) (*lab.Plan, bool, error) {
	type comp struct {
		class string
		id    sstz3.ID
		data  []byte
	}
	var comps []comp
	recipe, sz, _, err := sstz3.Encode(bytes.NewReader(f.data), int64(len(f.data)), opt, func(class string, id sstz3.ID, b []byte) error {
		comps = append(comps, comp{class, id, append([]byte(nil), b...)})
		return nil
	})
	if err != nil {
		if errors.Is(err, sstz3.ErrUnsupported) || errors.Is(err, sstz3.ErrCorrupt) || errors.Is(err, sstz3.ErrLimit) {
			p, err := cdcPlan(f)
			return p, true, err
		}
		return nil, false, err
	}
	p := &lab.Plan{MetaBytes: sz.Total() - sz.Digests, Detail: map[string]any{"sizes": sz}}
	for _, c := range comps {
		b := c.data
		p.Pieces = append(p.Pieces, lab.Piece{Comp: &lab.Comp{ID: lab.ID(c.id), Size: int64(len(b)), Class: c.class, Data: func() ([]byte, error) { return b, nil }}})
	}
	p.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
		return sstz3.Decode(w, recipe, func(id sstz3.ID) ([]byte, error) { return get(lab.ID(id)) }, sstz3.Limits{})
	}
	return p, false, nil
}

func hashCutter(target int) func() sstz3.Cutter {
	return func() sstz3.Cutter { return &sstz3.HashCutter{Target: target, Max: 4 * target} }
}

// canonicalStream returns the canonical logical record stream of a table
// (uvarint(len ukey) ukey uvarint(len val) val ...) and the end offset of every record.
func canonicalStream(f file) (stream []byte, ends []int, err error) {
	err = sstz3.Records(bytes.NewReader(f.data), int64(len(f.data)), sstz3.Limits{}, func(uk []byte, _ uint64, v []byte) error {
		stream = binary.AppendUvarint(stream, uint64(len(uk)))
		stream = append(stream, uk...)
		stream = binary.AppendUvarint(stream, uint64(len(v)))
		stream = append(stream, v...)
		ends = append(ends, len(stream))
		return nil
	})
	return
}

// recordCDCCutter chunks the canonical stream with production CDC and snaps every cut to
// the next record boundary.
func recordCDCCutter(f file) (sstz3.Cutter, error) {
	st, ends, err := canonicalStream(f)
	if err != nil {
		return nil, err
	}
	sizes, err := lab.CDCSizes(bytes.NewReader(st))
	if err != nil {
		return nil, err
	}
	var after []int
	pos, ri := 0, 0
	for _, z := range sizes {
		pos += z
		for ri < len(ends) && ends[ri] < pos {
			ri++
		}
		if ri < len(ends) && (len(after) == 0 || after[len(after)-1] != ri) {
			after = append(after, ri)
		}
	}
	return &sstz3.AtCutter{After: after}, nil
}

func sstRep(name string, form sstz3.Form, cut func() sstz3.Cutter) repDef {
	return repDef{name: name, plan: func(f file) (*lab.Plan, bool, error) {
		var c sstz3.Cutter
		if cut != nil {
			c = cut()
		}
		return sstPlan(f, sstz3.Options{Form: form, Cutter: c})
	}}
}

// oracleValues is the perfect exact-value-identity bound: one component per distinct
// value of the table, no keys, no trailers, no recipe.
func oracleValues(f file) (*lab.Plan, bool, error) {
	p := &lab.Plan{Oracle: true}
	err := sstz3.Records(bytes.NewReader(f.data), int64(len(f.data)), sstz3.Limits{}, func(_ []byte, _ uint64, v []byte) error {
		b := append([]byte(nil), v...)
		p.Pieces = append(p.Pieces, lab.Piece{Comp: &lab.Comp{ID: sid(b), Size: int64(len(b)), Class: "value", Data: func() ([]byte, error) { return b, nil }}, Via: true})
		return nil
	})
	if err != nil {
		q, err := cdcPlan(f)
		return q, true, err
	}
	return p, false, nil
}

// oracleKVCDC is production CDC over the canonical user-key/value byte stream with no
// recipe at all (an upper bound on what record-aware chunking could save).
func oracleKVCDC(f file) (*lab.Plan, bool, error) {
	st, _, err := canonicalStream(f)
	if err != nil {
		q, err := cdcPlan(f)
		return q, true, err
	}
	for _, r := range lab.Reps() {
		if r.Name == "cdc" {
			p, err := r.Analyze(&lab.Input{Family: "sst", Format: "kv", Revision: f.name, Src: lab.BytesSource(f.name, st), Opts: lab.DefaultOptions()})
			if p != nil {
				p.Oracle, p.Rebuild, p.MetaBytes = true, nil, 0
			}
			return p, false, err
		}
	}
	return nil, false, errors.New("no cdc")
}

func allReps(target int) []repDef {
	return []repDef{
		{name: "cdc", plan: func(f file) (*lab.Plan, bool, error) { p, err := cdcPlan(f); return p, false, err }},
		{name: "blocks", plan: func(f file) (*lab.Plan, bool, error) { return nativeBlocks(f, false) }},
		{name: "blocks-grp", plan: func(f file) (*lab.Plan, bool, error) { return nativeBlocks(f, true) }},
		sstRep("ublocks", sstz3.FormBlocks, nil),
		{name: "ukv-expl", plan: func(f file) (*lab.Plan, bool, error) {
			return sstPlan(f, sstz3.Options{Form: sstz3.FormUKV, Cutter: hashCutter(target)(), NoPredict: true})
		}},
		sstRep("rec1", sstz3.FormUKV, func() sstz3.Cutter { return sstz3.RecordCutter{} }),
		sstRep("blkrec", sstz3.FormUKV, nil),
		sstRep("full", sstz3.FormFull, hashCutter(target)),
		sstRep("ukv", sstz3.FormUKV, hashCutter(target)),
		sstRep("kv", sstz3.FormKV, hashCutter(target)),
		sstRep("ktv", sstz3.FormKTV, hashCutter(target)),
		sstRep("kvt", sstz3.FormKVT, hashCutter(target)),
		{name: "rcdc", plan: func(f file) (*lab.Plan, bool, error) {
			c, err := recordCDCCutter(f)
			if err != nil {
				p, err := cdcPlan(f)
				return p, true, err
			}
			return sstPlan(f, sstz3.Options{Form: sstz3.FormUKV, Cutter: c})
		}},
		{name: "o-value", oracle: true, plan: func(f file) (*lab.Plan, bool, error) { return oracleValues(f) }},
		{name: "o-kvcdc", oracle: true, plan: func(f file) (*lab.Plan, bool, error) { return oracleKVCDC(f) }},
	}
}

func sstRecords(r io.ReaderAt, size int64, fn func(uk []byte, tr uint64, v []byte) error) error {
	return sstz3.Records(r, size, sstz3.Limits{}, fn)
}
