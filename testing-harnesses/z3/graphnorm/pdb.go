package graphnorm

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

// Portable PDB adapter: the file is a metadata root, streams and padding. Fixed-width table
// rows become column-major varint streams (OpTable); heaps are opaque components; every
// other byte is literal. Replaying the generated program must reproduce the file exactly.

var errPDB = errors.New("graphnorm: unsupported Portable PDB")

type pdbCol struct {
	Name string
	W    int
	Heap byte // 's' strings, 'g' guid, 'b' blob, 't' table index, 0 plain
}

type pdbTable struct {
	ID   int
	Name string
	Cols []pdbCol
	Rows int
	Off  int // absolute file offset of first row
	W    int
}

// PDBEncoding is the factorization: a small program plus the streams it references.
type PDBEncoding struct {
	Program []byte
	Blobs   [][]byte // [0]=column streams (all tables), then heaps in order of BlobNames
	Names   []string
	Size    int
	Cols    []ColChoice
}

// ColChoice records the synthesizer's decision for one column (reported as evidence).
type ColChoice struct {
	Table, Col string
	Pred       int
	RawBytes   int
	DeltaBytes int
	Chosen     int
}

func uvarints(vs []uint64) []byte {
	var b []byte
	for _, v := range vs {
		b = binary.AppendUvarint(b, v)
	}
	return b
}

func parsePDB(d []byte) (tables []pdbTable, heaps map[string][2]int, litEnd int, err error) {
	le := binary.LittleEndian
	if len(d) < 32 || le.Uint32(d) != 0x424A5342 {
		return nil, nil, 0, errPDB
	}
	vlen := int(le.Uint32(d[12:]))
	if vlen > 4096 {
		return nil, nil, 0, errPDB
	}
	off := (16 + vlen + 3) &^ 3
	if off+4 > len(d) {
		return nil, nil, 0, errPDB
	}
	count := int(le.Uint16(d[off+2:]))
	off += 4
	if count > 32 {
		return nil, nil, 0, errPDB
	}
	heaps = map[string][2]int{}
	for i := 0; i < count; i++ {
		if off+8 > len(d) {
			return nil, nil, 0, errPDB
		}
		so, sz := int(le.Uint32(d[off:])), int(le.Uint32(d[off+4:]))
		e := bytes.IndexByte(d[off+8:min(len(d), off+8+32)], 0)
		if e < 0 || so < 0 || sz < 0 || so > len(d) || sz > len(d)-so {
			return nil, nil, 0, errPDB
		}
		name := string(d[off+8 : off+8+e])
		if _, dup := heaps[name]; dup {
			return nil, nil, 0, errPDB
		}
		heaps[name] = [2]int{so, so + sz}
		off += (8 + e + 1 + 3) &^ 3
	}
	pdbS, ok1 := heaps["#Pdb"]
	tabS, ok2 := heaps["#~"]
	if !ok1 || !ok2 || pdbS[1]-pdbS[0] < 32 || tabS[1]-tabS[0] < 24 {
		return nil, nil, 0, errPDB
	}
	b := d[pdbS[0]:pdbS[1]]
	var external [64]uint32
	mask := le.Uint64(b[24:])
	p := 32
	for i := 0; i < 64; i++ {
		if mask>>i&1 != 0 {
			if len(b)-p < 4 {
				return nil, nil, 0, errPDB
			}
			external[i] = le.Uint32(b[p:])
			p += 4
		}
	}
	if p != len(b) {
		return nil, nil, 0, errPDB
	}
	tb := d[tabS[0]:tabS[1]]
	valid := le.Uint64(tb[8:])
	if valid&^uint64(0xff000000000000) != 0 {
		return nil, nil, 0, errPDB
	}
	var rows [64]uint32
	p = 24
	for i := 0; i < 64; i++ {
		if valid>>i&1 != 0 {
			if len(tb)-p < 4 {
				return nil, nil, 0, errPDB
			}
			rows[i] = le.Uint32(tb[p:])
			if rows[i] > 1<<24 {
				return nil, nil, 0, errPDB
			}
			p += 4
		}
	}
	hsz := func(bit byte) int {
		if tb[6]&bit != 0 {
			return 4
		}
		return 2
	}
	str, guid, blob := hsz(1), hsz(2), hsz(4)
	ref := func(i int) int {
		if max(rows[i], external[i]) >= 65536 {
			return 4
		}
		return 2
	}
	coded := 2
	for _, i := range []int{6, 4, 1, 2, 8, 9, 10, 0, 14, 23, 20, 17, 26, 27, 32, 35, 38, 39, 40, 42, 44, 43, 48, 50, 51, 52, 53} {
		if max(rows[i], external[i]) >= 1<<(16-5) {
			coded = 4
		}
	}
	defs := []struct {
		name string
		cols []pdbCol
	}{
		{"Document", []pdbCol{{"Name", blob, 'b'}, {"HashAlg", guid, 'g'}, {"Hash", blob, 'b'}, {"Language", guid, 'g'}}},
		{"MethodDebugInformation", []pdbCol{{"Document", ref(48), 't'}, {"SequencePoints", blob, 'b'}}},
		{"LocalScope", []pdbCol{{"Method", ref(6), 't'}, {"ImportScope", ref(53), 't'}, {"VariableList", ref(51), 't'}, {"ConstantList", ref(52), 't'}, {"StartOffset", 4, 0}, {"Length", 4, 0}}},
		{"LocalVariable", []pdbCol{{"Attributes", 2, 0}, {"Index", 2, 0}, {"Name", str, 's'}}},
		{"LocalConstant", []pdbCol{{"Name", str, 's'}, {"Signature", blob, 'b'}}},
		{"ImportScope", []pdbCol{{"Parent", ref(53), 't'}, {"Imports", blob, 'b'}}},
		{"StateMachineMethod", []pdbCol{{"MoveNext", ref(6), 't'}, {"Kickoff", ref(6), 't'}}},
		{"CustomDebugInformation", []pdbCol{{"Parent", coded, 't'}, {"Kind", guid, 'g'}, {"Value", blob, 'b'}}},
	}
	for i, df := range defs {
		t := pdbTable{ID: 48 + i, Name: df.name, Cols: df.cols, Rows: int(rows[48+i]), Off: tabS[0] + p}
		for _, c := range df.cols {
			t.W += c.W
		}
		if uint64(t.Rows)*uint64(t.W) > uint64(len(tb)-p) {
			return nil, nil, 0, errPDB
		}
		p += t.Rows * t.W
		tables = append(tables, t)
	}
	return tables, heaps, tabS[0] + p, nil
}

type pdbSeg struct {
	lo, hi int
	kind   byte // 'L' literal, 'H' heap, 'T' table
	name   string
	t      int
}

// EncodePDB factors a Portable PDB. choose=false forces raw columns (no predictor search).
func EncodePDB(d []byte, search bool) (*PDBEncoding, error) {
	tables, streams, _, err := parsePDB(d)
	if err != nil {
		return nil, err
	}
	var segs []pdbSeg
	for ti, t := range tables {
		if t.Rows > 0 {
			segs = append(segs, pdbSeg{t.Off, t.Off + t.Rows*t.W, 'T', t.Name, ti})
		}
	}
	for name, sp := range streams {
		switch name {
		case "#Strings", "#Blob", "#GUID", "#US":
			if sp[1] > sp[0] {
				segs = append(segs, pdbSeg{sp[0], sp[1], 'H', name, 0})
			}
		}
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].lo < segs[j].lo })
	for i := 1; i < len(segs); i++ {
		if segs[i].lo < segs[i-1].hi {
			return nil, fmt.Errorf("%w: overlapping segments", errPDB)
		}
	}
	enc := &PDBEncoding{Size: len(d)}
	var cols []byte // blob 0
	var heapBlobs [][]byte
	var prog []byte
	lit := func(lo, hi int) {
		if hi > lo {
			prog = append(prog, OpLit)
			prog = binary.AppendUvarint(prog, uint64(hi-lo))
			prog = append(prog, d[lo:hi]...)
		}
	}
	pos := 0
	prog = append(prog, OpStream, 0, 0)
	for _, sg := range segs {
		lit(pos, sg.lo)
		switch sg.kind {
		case 'H':
			prog = append(prog, OpCopy)
			prog = binary.AppendUvarint(prog, uint64(1+len(heapBlobs)))
			prog = binary.AppendUvarint(prog, 0)
			prog = binary.AppendUvarint(prog, uint64(sg.hi-sg.lo))
			heapBlobs = append(heapBlobs, d[sg.lo:sg.hi])
			enc.Names = append(enc.Names, sg.name)
		case 'T':
			t := tables[sg.t]
			prog = append(prog, OpTable)
			prog = binary.AppendUvarint(prog, uint64(t.Rows))
			prog = binary.AppendUvarint(prog, uint64(len(t.Cols)))
			off := t.Off
			colVals := make([][]uint64, len(t.Cols))
			for c := range t.Cols {
				colVals[c] = make([]uint64, t.Rows)
			}
			for r := 0; r < t.Rows; r++ {
				for c, col := range t.Cols {
					var v uint64
					for k := col.W - 1; k >= 0; k-- {
						v = v<<8 | uint64(d[off+k])
					}
					colVals[c][r] = v
					off += col.W
				}
			}
			for c, col := range t.Cols {
				raw := uvarints(colVals[c])
				deltas := make([]uint64, t.Rows)
				var prev uint64
				for i, v := range colVals[c] {
					deltas[i] = Zig(int64(v - prev))
					prev = v
				}
				dl := uvarints(deltas)
				choice := ColChoice{Table: t.Name, Col: col.Name, RawBytes: len(raw), DeltaBytes: len(dl)}
				use := PredRaw
				body := raw
				if search && packedLenOf(dl) < packedLenOf(raw) {
					use, body = PredDelta, dl
				}
				choice.Chosen = use
				enc.Cols = append(enc.Cols, choice)
				prog = append(prog, byte(col.W), byte(use))
				cols = append(cols, body...)
			}
		}
		pos = sg.hi
	}
	lit(pos, len(d))
	enc.Program = prog
	enc.Blobs = append([][]byte{cols}, heapBlobs...)
	out, err := enc.Decode()
	if err != nil {
		return nil, fmt.Errorf("graphnorm: pdb self-replay: %w", err)
	}
	if !bytes.Equal(out, d) {
		return nil, errors.New("graphnorm: pdb self-replay mismatch")
	}
	return enc, nil
}

func (e *PDBEncoding) Decode() ([]byte, error) {
	m := NewByteMachine()
	m.Blobs = e.Blobs
	if err := m.Exec(e.Program); err != nil {
		return nil, err
	}
	if err := m.Finish(); err != nil {
		return nil, err
	}
	return m.Output(), nil
}
