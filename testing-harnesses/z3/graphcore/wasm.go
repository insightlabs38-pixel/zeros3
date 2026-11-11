package graphcore

import (
	"errors"
)

// Wasm control adapter (analysis only, no exact transform): parses the code section and
// abstracts function-index immediates. It exists to measure the opportunity the generic
// mechanism would have on a format whose references are unstable LEB128 indices.

var errWasm = errors.New("graphcore: unsupported wasm")

type WasmModule struct {
	Bodies   [][]byte // code-section function bodies (locals + expression)
	Names    []string // aligned with Bodies ("" if not exported)
	NumCalls int      // total call/ref.func immediates seen
}

func leb(b []byte) (v uint64, n int, ok bool) {
	var sh uint
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7F) << sh
		sh += 7
		if b[i]&0x80 == 0 {
			return v, i + 1, true
		}
	}
	return 0, 0, false
}

func ParseWasm(d []byte) (*WasmModule, error) {
	if len(d) < 8 || string(d[:4]) != "\x00asm" {
		return nil, errWasm
	}
	m := &WasmModule{}
	p := 8
	nImp := 0
	exportName := map[uint32]string{}
	for p < len(d) {
		id := d[p]
		sz, n, ok := leb(d[p+1:])
		if !ok || p+1+n+int(sz) > len(d) {
			return nil, errWasm
		}
		body := d[p+1+n : p+1+n+int(sz)]
		p += 1 + n + int(sz)
		switch id {
		case 2: // import
			cnt, k, _ := leb(body)
			q := k
			for i := uint64(0); i < cnt; i++ {
				for s := 0; s < 2; s++ {
					l, k2, ok := leb(body[q:])
					if !ok {
						return nil, errWasm
					}
					q += k2 + int(l)
				}
				kind := body[q]
				q++
				switch kind {
				case 0:
					_, k3, _ := leb(body[q:])
					q += k3
					nImp++
				case 1:
					q++
					fl := body[q]
					q++
					_, k3, _ := leb(body[q:])
					q += k3
					if fl&1 != 0 {
						_, k3, _ = leb(body[q:])
						q += k3
					}
				case 2:
					fl := body[q]
					q++
					_, k3, _ := leb(body[q:])
					q += k3
					if fl&1 != 0 {
						_, k3, _ = leb(body[q:])
						q += k3
					}
				case 3:
					q += 2
				default:
					return nil, errWasm
				}
			}
		case 7: // export
			cnt, k, _ := leb(body)
			q := k
			for i := uint64(0); i < cnt; i++ {
				l, k2, ok := leb(body[q:])
				if !ok || q+k2+int(l) >= len(body) {
					return nil, errWasm
				}
				name := string(body[q+k2 : q+k2+int(l)])
				q += k2 + int(l)
				kind := body[q]
				q++
				idx, k3, _ := leb(body[q:])
				q += k3
				if kind == 0 {
					exportName[uint32(idx)] = name
				}
			}
		case 10: // code
			cnt, k, _ := leb(body)
			q := k
			for i := uint64(0); i < cnt; i++ {
				l, k2, ok := leb(body[q:])
				if !ok || q+k2+int(l) > len(body) {
					return nil, errWasm
				}
				m.Bodies = append(m.Bodies, body[q+k2:q+k2+int(l)])
				m.Names = append(m.Names, exportName[uint32(nImp)+uint32(i)])
				q += k2 + int(l)
			}
		}
	}
	return m, nil
}

// funcKey maps a function index to a stable name when one exists.
type FuncNamer func(idx uint32) string

// WasmNamer builds the index->name map used for abstraction.
func (m *WasmModule) Namer(nImp int) FuncNamer {
	names := map[uint32]string{}
	for i, n := range m.Names {
		if n != "" {
			names[uint32(nImp+i)] = n
		}
	}
	return func(idx uint32) string { return names[idx] }
}

// AbstractBody copies a function body replacing call/ref.func immediates by the callee name
// (or leaving the raw LEB when the callee has no stable name). ok=false: unsupported opcode.
func AbstractBody(b []byte, name FuncNamer) (out []byte, calls int, ok bool) {
	p := 0
	// locals
	nl, k, ok2 := leb(b)
	if !ok2 {
		return nil, 0, false
	}
	p = k
	for i := uint64(0); i < nl; i++ {
		_, k1, o := leb(b[p:])
		if !o || p+k1 >= len(b) {
			return nil, 0, false
		}
		p += k1 + 1
	}
	out = append(out, b[:p]...)
	skipLEB := func() bool {
		_, k, o := leb(b[p:])
		if !o {
			return false
		}
		out = append(out, b[p:p+k]...)
		p += k
		return true
	}
	for p < len(b) {
		op := b[p]
		out = append(out, op)
		p++
		switch {
		case op == 0x10 || op == 0xD2:
			idx, k, o := leb(b[p:])
			if !o {
				return nil, 0, false
			}
			calls++
			if nm := name(uint32(idx)); nm != "" {
				out = append(out, 0xFF, byte(len(nm)))
				out = append(out, nm...)
			} else {
				out = append(out, b[p:p+k]...)
			}
			p += k
		case op == 0x11:
			if !skipLEB() || !skipLEB() {
				return nil, 0, false
			}
		case op == 0x02 || op == 0x03 || op == 0x04:
			if p >= len(b) {
				return nil, 0, false
			}
			if b[p] == 0x40 || b[p] >= 0x6F {
				out = append(out, b[p])
				p++
			} else if !skipLEB() {
				return nil, 0, false
			}
		case op == 0x0C || op == 0x0D || (op >= 0x20 && op <= 0x26):
			if !skipLEB() {
				return nil, 0, false
			}
		case op == 0x0E:
			cnt, k, o := leb(b[p:])
			if !o {
				return nil, 0, false
			}
			out = append(out, b[p:p+k]...)
			p += k
			for i := uint64(0); i <= cnt; i++ {
				if !skipLEB() {
					return nil, 0, false
				}
			}
		case op == 0x1C:
			cnt, k, o := leb(b[p:])
			if !o || p+k+int(cnt) > len(b) {
				return nil, 0, false
			}
			out = append(out, b[p:p+k+int(cnt)]...)
			p += k + int(cnt)
		case op >= 0x28 && op <= 0x3E:
			if !skipLEB() || !skipLEB() {
				return nil, 0, false
			}
		case op == 0x3F || op == 0x40 || op == 0xD0:
			if p >= len(b) {
				return nil, 0, false
			}
			out = append(out, b[p])
			p++
		case op == 0x41 || op == 0x42:
			if !skipLEB() {
				return nil, 0, false
			}
		case op == 0x43:
			if p+4 > len(b) {
				return nil, 0, false
			}
			out = append(out, b[p:p+4]...)
			p += 4
		case op == 0x44:
			if p+8 > len(b) {
				return nil, 0, false
			}
			out = append(out, b[p:p+8]...)
			p += 8
		case op == 0xFC:
			sub, k, o := leb(b[p:])
			if !o {
				return nil, 0, false
			}
			out = append(out, b[p:p+k]...)
			p += k
			switch sub {
			case 0, 1, 2, 3, 4, 5, 6, 7:
			case 8, 12:
				if !skipLEB() || !skipLEB() {
					return nil, 0, false
				}
			case 9, 13, 15, 16, 17:
				if !skipLEB() {
					return nil, 0, false
				}
			case 10, 14:
				if !skipLEB() || !skipLEB() {
					return nil, 0, false
				}
			case 11:
				if !skipLEB() {
					return nil, 0, false
				}
			default:
				return nil, 0, false
			}
		case op == 0xFD || (op >= 0xC5 && op < 0xD0) || op > 0xD2:
			return nil, 0, false
		}
	}
	return out, calls, true
}
