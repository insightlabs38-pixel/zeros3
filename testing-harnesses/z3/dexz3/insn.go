package dexz3

// Dalvik instruction widths and index-bearing operands (the finite table for DEX 035-041).
// width is in 16-bit code units; 0 marks an opcode that is not valid in a DEX file.
type opInfo struct {
	width uint8
	space [2]Space // index space of the (up to) two operands
	pos   [2]uint8 // code-unit position of each operand
	wide  bool     // first operand is 32 bits (const-string/jumbo)
}

var ops [256]opInfo

func init() {
	set := func(lo, hi int, w uint8) {
		for i := lo; i <= hi; i++ {
			ops[i].width = w
		}
	}
	ref := func(lo, hi int, w uint8, sp Space) {
		for i := lo; i <= hi; i++ {
			ops[i] = opInfo{width: w, space: [2]Space{sp, SpNone}, pos: [2]uint8{1, 0}}
		}
	}
	set(0x00, 0x00, 1) // nop (payload pseudo-instructions are recognised by the walker)
	set(0x01, 0x01, 1)
	set(0x02, 0x02, 2)
	set(0x03, 0x03, 3)
	set(0x04, 0x04, 1)
	set(0x05, 0x05, 2)
	set(0x06, 0x06, 3)
	set(0x07, 0x07, 1)
	set(0x08, 0x08, 2)
	set(0x09, 0x09, 3)
	set(0x0a, 0x11, 1) // move-result*, move-exception, return*
	set(0x12, 0x12, 1) // const/4
	set(0x13, 0x13, 2) // const/16
	set(0x14, 0x14, 3) // const
	set(0x15, 0x16, 2) // const/high16, const-wide/16
	set(0x17, 0x17, 3) // const-wide/32
	set(0x18, 0x18, 5) // const-wide
	set(0x19, 0x19, 2) // const-wide/high16
	ref(0x1a, 0x1a, 2, SpString)
	ref(0x1b, 0x1b, 3, SpString)
	ops[0x1b].wide = true
	ref(0x1c, 0x1c, 2, SpType) // const-class
	set(0x1d, 0x1e, 1)         // monitor-*
	ref(0x1f, 0x20, 2, SpType) // check-cast, instance-of
	set(0x21, 0x21, 1)         // array-length
	ref(0x22, 0x23, 2, SpType) // new-instance, new-array
	ref(0x24, 0x25, 3, SpType) // filled-new-array(/range)
	set(0x26, 0x26, 3)         // fill-array-data
	set(0x27, 0x27, 1)         // throw
	set(0x28, 0x28, 1)         // goto
	set(0x29, 0x29, 2)         // goto/16
	set(0x2a, 0x2a, 3)         // goto/32
	set(0x2b, 0x2c, 3)         // packed-switch, sparse-switch
	set(0x2d, 0x31, 2)         // cmp*
	set(0x32, 0x3d, 2)         // if-*, if-*z
	set(0x44, 0x51, 2)         // aget/aput
	ref(0x52, 0x5f, 2, SpField)
	ref(0x60, 0x6d, 2, SpField)
	ref(0x6e, 0x72, 3, SpMethod)
	ref(0x74, 0x78, 3, SpMethod)
	set(0x7b, 0x8f, 1)
	set(0x90, 0xaf, 2)
	set(0xb0, 0xcf, 1)
	set(0xd0, 0xe2, 2)
	ops[0xfa] = opInfo{width: 4, space: [2]Space{SpMethod, SpProto}, pos: [2]uint8{1, 3}}
	ops[0xfb] = opInfo{width: 4, space: [2]Space{SpMethod, SpProto}, pos: [2]uint8{1, 3}}
	// invoke-custom, const-method-handle: call-site / method-handle tables are not modelled -> operands stay raw
	set(0xfc, 0xfd, 3)
	set(0xfe, 0xfe, 2)
	ref(0xff, 0xff, 2, SpProto) // const-method-type
}

// payload pseudo-instruction identifiers (first code unit of a nop-prefixed payload)
const (
	payloadPacked = 0x0100
	payloadSparse = 0x0200
	payloadArray  = 0x0300
)

// WalkInsns calls fn(unitIndex, space, width32) for every index-bearing operand of an instruction
// stream (units are the little-endian 16-bit code units). Malformed streams return ErrFormat.
func walkInsns(code []byte, n int, fn func(unit int, sp Space, wide bool) error) error {
	if len(code) < 2*n {
		return bad("insns truncated")
	}
	u16 := func(i int) uint32 { return le16(code[2*i:]) }
	for i := 0; i < n; {
		w := u16(i)
		op := int(w & 0xff)
		if op == 0 && w != 0 { // pseudo payload
			var units uint64
			switch w {
			case payloadPacked:
				if i+2 > n {
					return bad("payload truncated")
				}
				units = uint64(u16(i+1))*2 + 4
			case payloadSparse:
				if i+2 > n {
					return bad("payload truncated")
				}
				units = uint64(u16(i+1))*4 + 2
			case payloadArray:
				if i+4 > n {
					return bad("payload truncated")
				}
				ew, sz := uint64(u16(i+1)), uint64(u16(i+2))|uint64(u16(i+3))<<16
				units = (sz*ew+1)/2 + 4
				if ew > 8 {
					return bad("array payload element width %d", ew)
				}
			default:
				return bad("unknown pseudo-instruction %#x", w)
			}
			if units > uint64(n-i) {
				return bad("payload overruns")
			}
			i += int(units)
			continue
		}
		oi := &ops[op]
		if oi.width == 0 {
			return bad("invalid opcode %#x", op)
		}
		if i+int(oi.width) > n {
			return bad("instruction overruns")
		}
		for k := 0; k < 2; k++ {
			if oi.space[k] != SpNone {
				if err := fn(i+int(oi.pos[k]), oi.space[k], oi.wide && k == 0); err != nil {
					return err
				}
			}
		}
		i += int(oi.width)
	}
	return nil
}
