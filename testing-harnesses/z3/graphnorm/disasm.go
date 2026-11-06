package graphnorm

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Disasm renders a template/skeleton op stream as text (diagnostics and reports).
func Disasm(b []byte, info map[uint64][]Abbrev) []string {
	var scopes [][]Abbrev
	var bids []uint64
	_ = bids
	r := &opReader{b: b}
	var out []string
	tok := func() string {
		t := r.uv()
		p := t >> 3
		switch t & 7 {
		case TokLit:
			return fmt.Sprint(p)
		case TokEsc:
			return fmt.Sprintf("E%d", r.uv())
		case TokXRel:
			return fmt.Sprintf("xrel[%d]", p)
		case TokXAbs:
			return fmt.Sprintf("xabs[%d]", p)
		case TokXAb1:
			return fmt.Sprintf("xabs1[%d]", p)
		case TokLAbs:
			return fmt.Sprintf("labs+%d", p)
		case TokXRlS:
			return fmt.Sprintf("xrels[%d]", p)
		case TokLMD:
			return fmt.Sprintf("lmd+%d/%d", p>>1, p&1)
		}
		return "?"
	}
	for r.pos < len(b) && r.err == nil {
		op := r.byte()
		switch op {
		case OpExt:
			var parts []string
			for t := 0; t < NumTabs; t++ {
				n := r.uv()
				for i := uint64(0); i < n && r.err == nil; i++ {
					parts = append(parts, fmt.Sprintf("%d:%x", t, r.bytes(KeyLen)))
				}
			}
			out = append(out, "EXT "+strings.Join(parts, " "))
		case OpSetInst:
			out = append(out, fmt.Sprintf("SETINST %d", r.uv()))
		case OpEnter:
			id, w := r.uv(), r.uv()
			scopes = append(scopes, append([]Abbrev(nil), info[id]...))
			out = append(out, fmt.Sprintf("ENTER %d w%d", id, w))
		case OpEnd:
			scopes = scopes[:len(scopes)-1]
			out = append(out, "END")
		case OpDefAbb:
			n := r.uv()
			var a Abbrev
			for i := uint64(0); i < n; i++ {
				fl := r.byte()
				v := r.uv()
				if fl == 0 {
					a = append(a, AbbrevOp{Lit: true, Val: v})
				} else {
					a = append(a, AbbrevOp{Enc: fl, Width: v})
				}
			}
			if len(scopes) > 0 {
				scopes[len(scopes)-1] = append(scopes[len(scopes)-1], a)
			}
			out = append(out, "DEFABBR")
		default:
			if op < 0x80 {
				out = append(out, fmt.Sprintf("op %#x", op))
				return out
			}
			ab := uint64(op & 63)
			if ab == 63 {
				ab = r.uv()
			}
			var parts []string
			var code uint64
			blob := 0
			if ab == 3 {
				code = r.uv()
				n := r.uv()
				for i := uint64(0); i < n && r.err == nil; i++ {
					parts = append(parts, tok())
				}
			} else if len(scopes) > 0 && ab-4 < uint64(len(scopes[len(scopes)-1])) {
				a := scopes[len(scopes)-1][ab-4]
				if a[0].Lit {
					code = a[0].Val
				} else {
					code = r.uv()
				}
				for i := 1; i < len(a) && r.err == nil; i++ {
					o := a[i]
					switch {
					case !o.Lit && o.Enc == encArray:
						n := r.uv()
						for j := uint64(0); j < n && r.err == nil; j++ {
							parts = append(parts, tok())
						}
						i = len(a)
					case !o.Lit && o.Enc == encBlob:
						blob = int(r.uv())
						r.bytes(uint64(blob))
					case o.Lit:
						parts = append(parts, fmt.Sprint(o.Val))
					default:
						parts = append(parts, tok())
					}
				}
			} else {
				out = append(out, "REC <unknown abbrev>")
				return out
			}
			s := fmt.Sprintf("REC a%d c%d [%s]", ab, code, strings.Join(parts, " "))
			if op&0x40 != 0 {
				s += " DEF"
			}
			if blob > 0 {
				s += fmt.Sprintf(" blob%d", blob)
			}
			out = append(out, s)
		case OpLit:
			n := r.uv()
			r.bytes(n)
			out = append(out, fmt.Sprintf("LIT %d", n))
		}
	}
	_ = binary.MaxVarintLen64
	return out
}
