package dexz3

import (
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"hash/adler32"
	"sort"
)

// A small DEX writer for tests: it lays out the id tables, one data region (code items, type
// lists, string data, class data) and the map list the way D8 does, with the header checksum
// and signature filled in. It exists so malformed and boundary cases can be built exactly.

type tProto struct {
	shorty, ret int
	params      []int
}
type tField struct{ class, typ, name int }
type tMethod struct{ class, proto, name int }
type tMember struct {
	idx   int // absolute field/method index
	flags uint32
	code  *tCode
	raw   []byte // overrides the ULEB bytes of the index delta (non-canonical encodings)
}
type tTry struct {
	start, count, handler int
}
type tCode struct {
	regs, ins, outs int
	insns           []uint16
	tries           []tTry
	handlerList     []byte // encoded_catch_handler_list, caller built
}
type tClass struct {
	typ, super, src    int // super/src -1 = NO_INDEX
	ifaces             []int
	sfields, ifields   []tMember
	dmethods, vmethods []tMember
}

type tDex struct {
	version string
	strs    []string
	types   []int
	protos  []tProto
	fields  []tField
	methods []tMethod
	classes []tClass
	// mutate lets a test corrupt the finished image before the checksum is computed
	mutate func(b []byte)
	// keepChecksum skips recomputing checksum and signature
	badChecksum bool
}

func uleb(v uint32) []byte {
	var b []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b = append(b, c|0x80)
			continue
		}
		return append(b, c)
	}
}

func mutf8(s string) []byte {
	b := uleb(uint32(len(s)))
	b = append(b, s...)
	return append(b, 0)
}

func put32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }
func put16(b []byte, v uint16) []byte { return binary.LittleEndian.AppendUint16(b, v) }

func pad(b []byte, a int) []byte {
	for len(b)%a != 0 {
		b = append(b, 0)
	}
	return b
}

func (d *tDex) build() []byte {
	ver := d.version
	if ver == "" {
		ver = "035"
	}
	ns, nt, np, nf, nm, nc := len(d.strs), len(d.types), len(d.protos), len(d.fields), len(d.methods), len(d.classes)
	off := 112
	sOff := off
	off += 4 * ns
	tOff := off
	off += 4 * nt
	pOff := off
	off += 12 * np
	fOff := off
	off += 8 * nf
	mOff := off
	off += 8 * nm
	cOff := off
	off += 32 * nc
	dataStart := off
	// data region: code items, type lists, string data, class data
	var data []byte
	at := func() int { return dataStart + len(data) }
	type sect struct {
		typ   uint16
		count int
		off   int
	}
	var secs []sect
	// code items
	codeOff := map[*tCode]int{}
	codeStart, codeN := at(), 0
	for _, c := range d.classes {
		for _, ms := range [][]tMember{c.dmethods, c.vmethods} {
			for _, m := range ms {
				if m.code == nil {
					continue
				}
				data = pad(data, 4)
				if dataStart%4 != 0 {
					panic("dataStart alignment")
				}
				codeOff[m.code] = at()
				cd := m.code
				data = put16(data, uint16(cd.regs))
				data = put16(data, uint16(cd.ins))
				data = put16(data, uint16(cd.outs))
				data = put16(data, uint16(len(cd.tries)))
				data = put32(data, 0)
				data = put32(data, uint32(len(cd.insns)))
				for _, u := range cd.insns {
					data = put16(data, u)
				}
				if len(cd.tries) > 0 {
					if len(cd.insns)%2 == 1 {
						data = put16(data, 0)
					}
					for _, t := range cd.tries {
						data = put32(data, uint32(t.start))
						data = put16(data, uint16(t.count))
						data = put16(data, uint16(t.handler))
					}
					data = append(data, cd.handlerList...)
				}
				codeN++
			}
		}
	}
	if codeN > 0 {
		secs = append(secs, sect{TypeCode, codeN, codeStart})
	}
	// type lists (proto parameters and class interfaces)
	listOff := map[string]int{}
	tlStart, tlN := at(), 0
	addList := func(l []int) int {
		if len(l) == 0 {
			return 0
		}
		k := fmt.Sprint(l)
		if o, ok := listOff[k]; ok {
			return o
		}
		data = pad(data, 4)
		o := at()
		listOff[k] = o
		data = put32(data, uint32(len(l)))
		for _, t := range l {
			data = put16(data, uint16(t))
		}
		tlN++
		return o
	}
	protoParams := make([]int, np)
	for i, p := range d.protos {
		protoParams[i] = addList(p.params)
	}
	ifaceOff := make([]int, nc)
	for i, c := range d.classes {
		ifaceOff[i] = addList(c.ifaces)
	}
	if tlN > 0 {
		secs = append(secs, sect{TypeTypeList, tlN, tlStart})
	}
	// string data
	strOff := make([]int, ns)
	ssStart := at()
	for i, s := range d.strs {
		strOff[i] = at()
		data = append(data, mutf8(s)...)
	}
	if ns > 0 {
		secs = append(secs, sect{TypeStringData, ns, ssStart})
	}
	// class data
	cdOff := make([]int, nc)
	cdStart, cdN := at(), 0
	for i, c := range d.classes {
		if len(c.sfields)+len(c.ifields)+len(c.dmethods)+len(c.vmethods) == 0 {
			continue
		}
		cdOff[i] = at()
		cdN++
		data = append(data, uleb(uint32(len(c.sfields)))...)
		data = append(data, uleb(uint32(len(c.ifields)))...)
		data = append(data, uleb(uint32(len(c.dmethods)))...)
		data = append(data, uleb(uint32(len(c.vmethods)))...)
		for g, ms := range [][]tMember{c.sfields, c.ifields, c.dmethods, c.vmethods} {
			prev := 0
			for _, m := range ms {
				if m.raw != nil {
					data = append(data, m.raw...)
				} else {
					data = append(data, uleb(uint32(m.idx-prev))...)
				}
				prev = m.idx
				data = append(data, uleb(m.flags)...)
				if g >= 2 {
					co := 0
					if m.code != nil {
						co = codeOff[m.code]
					}
					data = append(data, uleb(uint32(co))...)
				}
			}
		}
	}
	if cdN > 0 {
		secs = append(secs, sect{TypeClassData, cdN, cdStart})
	}
	// assemble
	var out []byte
	out = append(out, "dex\n"...)
	out = append(out, ver...)
	out = append(out, 0)
	out = append(out, make([]byte, 4+20)...) // checksum, signature
	mapOff := dataStart + len(data)
	mapOff = (mapOff + 3) &^ 3
	hdr := []uint32{0, 112, 0x12345678, 0, 0, uint32(mapOff),
		uint32(ns), uint32(sOff), uint32(nt), uint32(tOff), uint32(np), uint32(pOff), uint32(nf), uint32(fOff), uint32(nm), uint32(mOff), uint32(nc), uint32(cOff), 0, uint32(dataStart)}
	for _, v := range hdr {
		out = put32(out, v)
	}
	for i := 0; i < ns; i++ {
		out = put32(out, uint32(strOff[i]))
	}
	for _, t := range d.types {
		out = put32(out, uint32(t))
	}
	for i, p := range d.protos {
		out = put32(out, uint32(p.shorty))
		out = put32(out, uint32(p.ret))
		out = put32(out, uint32(protoParams[i]))
	}
	for _, f := range d.fields {
		out = put16(out, uint16(f.class))
		out = put16(out, uint16(f.typ))
		out = put32(out, uint32(f.name))
	}
	for _, m := range d.methods {
		out = put16(out, uint16(m.class))
		out = put16(out, uint16(m.proto))
		out = put32(out, uint32(m.name))
	}
	ni := uint32(NoIndex)
	for i, c := range d.classes {
		sup, src := ni, ni
		if c.super >= 0 {
			sup = uint32(c.super)
		}
		if c.src >= 0 {
			src = uint32(c.src)
		}
		for _, v := range []uint32{uint32(c.typ), 1, sup, uint32(ifaceOff[i]), src, 0, uint32(cdOff[i]), 0} {
			out = put32(out, v)
		}
	}
	out = append(out, data...)
	out = pad(out, 4)
	// map list
	type mi struct {
		typ   uint16
		count int
		off   int
	}
	var ms []mi
	ms = append(ms, mi{TypeHeader, 1, 0})
	for _, a := range []struct {
		t   uint16
		n   int
		off int
	}{{TypeStringID, ns, sOff}, {TypeTypeID, nt, tOff}, {TypeProtoID, np, pOff}, {TypeFieldID, nf, fOff}, {TypeMethodID, nm, mOff}, {TypeClassDef, nc, cOff}} {
		if a.n > 0 {
			ms = append(ms, mi{a.t, a.n, a.off})
		}
	}
	for _, s := range secs {
		ms = append(ms, mi{s.typ, s.count, s.off})
	}
	ms = append(ms, mi{TypeMapList, 1, mapOff})
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].off < ms[j].off })
	out = put32(out, uint32(len(ms)))
	for _, m := range ms {
		out = put16(out, m.typ)
		out = put16(out, 0)
		out = put32(out, uint32(m.count))
		out = put32(out, uint32(m.off))
	}
	binary.LittleEndian.PutUint32(out[32:], uint32(len(out))) // file_size
	binary.LittleEndian.PutUint32(out[104:], uint32(len(out)-dataStart))
	if d.mutate != nil {
		d.mutate(out)
	}
	if !d.badChecksum {
		sig := sha1.Sum(out[32:])
		copy(out[12:], sig[:])
		binary.LittleEndian.PutUint32(out[8:], adler32.Checksum(out[12:]))
	}
	return out
}

// small returns a valid DEX with one class "LA;", a void() method running the given code, and the
// strings/types/protos/methods it needs. extraStrings are appended to the (sorted) string table.
func small(code []uint16, extra ...string) *tDex {
	strs := append([]string{"LA;", "V", "run"}, extra...)
	sort.Strings(strs)
	idx := map[string]int{}
	for i, s := range strs {
		idx[s] = i
	}
	d := &tDex{strs: strs, types: []int{idx["LA;"], idx["V"]}}
	d.protos = []tProto{{shorty: idx["V"], ret: 1}}
	d.fields = []tField{{0, 1, idx["run"]}}
	d.methods = []tMethod{{0, 0, idx["run"]}}
	c := tClass{typ: 0, super: -1, src: -1}
	c.dmethods = []tMember{{idx: 0, flags: 1, code: &tCode{regs: 2, ins: 0, outs: 1, insns: code}}}
	d.classes = []tClass{c}
	return d
}
