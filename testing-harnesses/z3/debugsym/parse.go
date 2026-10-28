// Package debugsym studies exact debug-container partitions, without new wire kinds.
package debugsym

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

const MaxFile int64 = 256 << 20
const MaxDirectory = 32 << 20

var le = binary.LittleEndian

type Span struct {
	Off, End int64
	Name     string
}
type Info struct {
	Format  string
	Spans   []Span
	Streams int
	Rows    map[string]uint32
}

func bad(s string) error { return fmt.Errorf("debugsym: %s", s) }
func read(r io.ReaderAt, size, off, n int64) ([]byte, error) {
	if n < 0 || n > MaxDirectory || off < 0 || off > size || n > size-off {
		return nil, bad("read bounds")
	}
	b := make([]byte, n)
	got, err := r.ReadAt(b, off)
	if got != len(b) {
		return nil, fmt.Errorf("debugsym: short read %d/%d: %v", got, len(b), err)
	}
	return b, nil
}
func Parse(r io.ReaderAt, size int64) (*Info, error) {
	if size < 4 || size > MaxFile {
		return nil, bad("file size bound")
	}
	h, e := read(r, size, 0, min(size, 32))
	if e != nil {
		return nil, e
	}
	switch {
	case bytes.HasPrefix(h, []byte("Microsoft C/C++ MSF 7.00\r\n\x1aDS\x00\x00\x00")):
		return parseMSF(r, size)
	case string(h[:4]) == "BSJB":
		return parsePortable(r, size)
	case string(h[:4]) == "\x7fELF":
		return parseELF(r, size)
	default:
		return nil, bad("unsupported format")
	}
}

// Fill every opaque gap, including unused MSF pages, and reject overlap.
func partition(in *Info, size int64) error {
	sort.Slice(in.Spans, func(i, j int) bool { return in.Spans[i].Off < in.Spans[j].Off })
	var out []Span
	var at int64
	for _, s := range in.Spans {
		if s.Off < at || s.End < s.Off || s.End > size {
			return bad("overlapping/outside span")
		}
		if s.End == s.Off {
			continue
		}
		if s.Off > at {
			out = append(out, Span{at, s.Off, "opaque"})
		}
		out = append(out, s)
		at = s.End
	}
	if at < size {
		out = append(out, Span{at, size, "opaque"})
	}
	in.Spans = out
	return nil
}
func parseMSF(r io.ReaderAt, size int64) (*Info, error) {
	h, e := read(r, size, 0, 56)
	if e != nil {
		return nil, e
	}
	bs, free, nb, nd, res, mp := le.Uint32(h[32:]), le.Uint32(h[36:]), le.Uint32(h[40:]), le.Uint32(h[44:]), le.Uint32(h[48:]), le.Uint32(h[52:])
	if bs < 512 || bs > 65536 || bs&(bs-1) != 0 || uint64(nb)*uint64(bs) != uint64(size) || free < 1 || free > 2 || res != 0 || nd > MaxDirectory || nb < 3 {
		return nil, bad("MSF superblock")
	}
	pages := (uint64(nd) + uint64(bs) - 1) / uint64(bs)
	if pages*4 > uint64(bs) || mp >= nb || mp == 0 {
		return nil, bad("MSF directory map bound")
	}
	used := map[uint32]string{0: "superblock", mp: "block-map"}
	for first := uint64(0); first < uint64(nb); first += uint64(bs) {
		for _, f := range []uint64{1, 2} {
			if first+f < uint64(nb) {
				id := uint32(first + f)
				if _, ok := used[id]; ok {
					return nil, bad("MSF FPM overlap")
				}
				used[id] = "free-map"
			}
		}
	}
	claim := func(id uint32, name string) error {
		if id >= nb {
			return bad("MSF page outside file")
		}
		if _, ok := used[id]; ok {
			return bad("MSF duplicate page")
		}
		used[id] = name
		return nil
	}
	pm, e := read(r, size, int64(mp)*int64(bs), int64(pages*4))
	if e != nil {
		return nil, e
	}
	directory := make([]byte, 0, nd)
	for i := uint64(0); i < pages; i++ {
		id := le.Uint32(pm[i*4:])
		if e := claim(id, "directory"); e != nil {
			return nil, e
		}
		b, e := read(r, size, int64(id)*int64(bs), min(int64(bs), int64(nd)-int64(len(directory))))
		if e != nil {
			return nil, e
		}
		directory = append(directory, b...)
	}
	if len(directory) < 4 {
		return nil, bad("MSF missing stream count")
	}
	count := le.Uint32(directory)
	if count > 65536 || uint64(count)*4 > uint64(len(directory)-4) {
		return nil, bad("MSF stream count")
	}
	pos := 4 + int(count)*4
	for i := uint32(0); i < count; i++ {
		n := le.Uint32(directory[4+i*4:])
		if n == 0xffffffff {
			continue
		}
		blocks := (uint64(n) + uint64(bs) - 1) / uint64(bs)
		if blocks > uint64(nb) || blocks*4 > uint64(len(directory)-pos) {
			return nil, bad("MSF stream block list")
		}
		for k := uint64(0); k < blocks; k++ {
			id := le.Uint32(directory[pos:])
			pos += 4
			if e := claim(id, fmt.Sprintf("stream-%d", i)); e != nil {
				return nil, e
			}
		}
	}
	if pos != len(directory) {
		return nil, bad("MSF trailing directory bytes")
	}
	out := &Info{Format: "MSF7", Streams: int(count), Rows: map[string]uint32{}}
	for id, name := range used {
		out.Spans = append(out.Spans, Span{int64(id) * int64(bs), int64(id+1) * int64(bs), name})
	}
	return out, partition(out, size)
}
func parseELF(r io.ReaderAt, size int64) (*Info, error) {
	f, e := elf.NewFile(io.NewSectionReader(r, 0, size))
	if e != nil {
		return nil, e
	}
	defer f.Close()
	if len(f.Sections) > 65536 {
		return nil, bad("ELF sections bound")
	}
	out := &Info{Format: "ELF/DWARF", Rows: map[string]uint32{}}
	for _, s := range f.Sections {
		if s.Type == elf.SHT_NOBITS || s.FileSize == 0 || s.Type == elf.SHT_NULL {
			continue
		}
		if s.Offset > uint64(size) || s.FileSize > uint64(size)-s.Offset {
			return nil, bad("ELF section outside file")
		}
		out.Spans = append(out.Spans, Span{int64(s.Offset), int64(s.Offset + s.FileSize), s.Name})
	}
	return out, partition(out, size)
}

// Portable PDB has distinct table schemas; #Pdb supplies external type-system counts.
func parsePortable(r io.ReaderAt, size int64) (*Info, error) {
	h, e := read(r, size, 0, 16)
	if e != nil {
		return nil, e
	}
	version := le.Uint32(h[12:])
	if version > 4096 {
		return nil, bad("PDB version string")
	}
	off := (int64(16) + int64(version) + 3) &^ 3
	h, e = read(r, size, off, 4)
	if e != nil {
		return nil, e
	}
	count := int(le.Uint16(h[2:]))
	if count > 32 {
		return nil, bad("PDB stream count")
	}
	off += 4
	out := &Info{Format: "PortablePDB", Streams: count, Rows: map[string]uint32{}}
	streams := map[string]Span{}
	for i := 0; i < count; i++ {
		h, e = read(r, size, off, 8)
		if e != nil {
			return nil, e
		}
		start, n := int64(le.Uint32(h)), int64(le.Uint32(h[4:]))
		name, e := read(r, size, off+8, min(int64(32), size-off-8))
		if e != nil {
			return nil, e
		}
		end := bytes.IndexByte(name, 0)
		if end < 0 {
			return nil, bad("PDB stream name")
		}
		key := string(name[:end])
		if _, ok := streams[key]; ok {
			return nil, bad("duplicate PDB stream")
		}
		if start > size || n > size-start {
			return nil, bad("PDB stream bounds")
		}
		streams[key] = Span{start, start + n, key}
		off += (int64(8+end+1) + 3) &^ 3
	}
	out.Spans = []Span{{0, off, "metadata-root"}}
	for _, s := range streams {
		out.Spans = append(out.Spans, s)
	}
	if e := partition(out, size); e != nil {
		return nil, e
	}
	var external [64]uint32
	pdb, ok := streams["#Pdb"]
	if !ok {
		return nil, bad("missing #Pdb")
	}
	b, e := read(r, size, pdb.Off, pdb.End-pdb.Off)
	if e != nil {
		return nil, e
	}
	if len(b) < 32 {
		return nil, bad("short #Pdb")
	}
	mask := le.Uint64(b[24:])
	p := 32
	for i := 0; i < 64; i++ {
		if mask>>i&1 == 0 {
			continue
		}
		if len(b)-p < 4 {
			return nil, bad("PDB external rows")
		}
		external[i] = le.Uint32(b[p:])
		p += 4
	}
	if p != len(b) {
		return nil, bad("PDB external row trailing bytes")
	}
	tables, ok := streams["#~"]
	if !ok {
		return nil, bad("missing PDB tables")
	}
	b, e = read(r, size, tables.Off, tables.End-tables.Off)
	if e != nil {
		return nil, e
	}
	if len(b) < 24 {
		return nil, bad("short table header")
	}
	valid := le.Uint64(b[8:])
	if valid&^uint64(0xff000000000000) != 0 {
		return nil, bad("non-PDB tables")
	}
	var rows [64]uint32
	p = 24
	for i := 0; i < 64; i++ {
		if valid>>i&1 == 0 {
			continue
		}
		if len(b)-p < 4 {
			return nil, bad("table counts")
		}
		rows[i] = le.Uint32(b[p:])
		if rows[i] > 1<<24 {
			return nil, bad("table row limit")
		}
		p += 4
	}
	heap := func(bit byte) int {
		if b[6]&bit != 0 {
			return 4
		}
		return 2
	}
	str, guid, blob := heap(1), heap(2), heap(4)
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
	widths := []int{blob + guid + blob + guid, ref(48) + blob, ref(6) + ref(53) + ref(51) + ref(52) + 8, 4 + str, str + blob, ref(53) + blob, ref(6) * 2, coded + guid + blob}
	names := []string{"Document", "MethodDebugInformation", "LocalScope", "LocalVariable", "LocalConstant", "ImportScope", "StateMachineMethod", "CustomDebugInformation"}
	units := []Span{{tables.Off, tables.Off + int64(p), "table-header"}}
	for i := 0; i < 8; i++ {
		n := rows[48+i]
		out.Rows[names[i]] = n
		w := widths[i]
		if uint64(n)*uint64(w) > uint64(len(b)-p) {
			return nil, bad("PDB rows outside stream")
		}
		if n > 1<<18 {
			return nil, bad("PDB row-component limit")
		}
		for j := uint32(0); j < n; j++ {
			units = append(units, Span{tables.Off + int64(p), tables.Off + int64(p+w), names[i]})
			p += w
		}
	}
	if p < len(b) {
		units = append(units, Span{tables.Off + int64(p), tables.End, "table-padding"})
	}
	var all []Span
	for _, s := range out.Spans {
		if s.Off == tables.Off && s.End == tables.End && s.Name == tables.Name {
			all = append(all, units...)
		} else {
			all = append(all, s)
		}
	}
	out.Spans = all
	return out, partition(out, size)
}
