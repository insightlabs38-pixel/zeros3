// Package sendz3 characterizes legacy ZFS v1 streams without restoring a filesystem.
package sendz3

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"zeros3-z3/lab"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

const MaxSource = 128 << 20
const MaxPayload = 16 << 20
const MaxRecords = 262144

// Payloads accepts only little-endian legacy v1 records. Modern/unknown features
// are opaque fallback, never guessed. The final Fletcher4 verifies the preceding stream bytes.
func Payloads(data []byte) ([][2]int64, error) {
	bad := func() ([][2]int64, error) { return nil, fmt.Errorf("unsupported or invalid legacy ZFS stream") }
	if len(data) < 624 || len(data) > MaxSource || len(data)%4 != 0 {
		return bad()
	}
	u32 := func(p int) uint32 { return binary.LittleEndian.Uint32(data[p:]) }
	u64 := func(p int) uint64 { return binary.LittleEndian.Uint64(data[p:]) }
	if u32(0) != 0 || u32(4) != 0 || u64(8) != 0x2f5bacbac || u64(16) != 1 {
		return bad()
	}
	guid := u64(40)
	if guid == 0 {
		return bad()
	}
	var spans [][2]int64
	count := 0
	for off := 0; off < len(data); {
		count++
		if count > MaxRecords || len(data)-off < 312 || u32(off+4) != 0 {
			return bad()
		}
		typ := u32(off)
		size := uint64(0)
		goff := 0
		switch typ {
		case 0:
			if off != 0 {
				return bad()
			}
			goff = 40
		case 1:
			size = (uint64(u32(off+28)) + 7) &^ 7
			goff = 40
		case 2:
			goff = 24
		case 3:
			size = u64(off + 32)
			goff = 40
		case 4:
			goff = 32
		case 5:
			if off+312 != len(data) || u64(off+40) != guid {
				return bad()
			}
			// Legacy END has no payload; require its unused union tail to be zero.
			for _, v := range data[off+48 : off+312] {
				if v != 0 {
					return bad()
				}
			}
			var sum [4]uint64
			for p := 0; p < off; p += 4 {
				sum[0] += uint64(u32(p))
				sum[1] += sum[0]
				sum[2] += sum[1]
				sum[3] += sum[2]
			}
			for n, v := range sum {
				if u64(off+8+n*8) != v {
					return bad()
				}
			}
			return spans, nil
		default:
			return bad()
		}
		if u64(off+goff) != guid || size > MaxPayload || size%8 != 0 || size > uint64(len(data)-off-312) {
			return bad()
		}
		if size > 0 {
			spans = append(spans, [2]int64{int64(off + 312), int64(size)})
		}
		off += 312 + int(size)
	}
	return bad()
}

// Encode persists every byte; format-local external snapshot GUIDs remain opaque
// header bytes. Reconstruction needs no snapshot, filesystem or producer tool.
func Encode(data []byte) (*kinds.Bundle, error) {
	s, e := Payloads(data)
	if e != nil {
		return nil, e
	}
	return native(data, s)
}
func native(data []byte, spans [][2]int64) (*kinds.Bundle, error) {
	r := &repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(data))}
	var objects [][]byte
	add := func(b []byte) {
		if len(b) == 0 {
			return
		}
		objects = append(objects, b)
		r.Members = append(r.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(b), Size: int64(len(b))})
	}
	gap := func(b []byte) {
		for _, p := range lab.ChunkScaled(b, 1<<10, 4<<10, 16<<10) {
			add(p)
		}
	}
	off := int64(0)
	for _, s := range spans {
		if s[0] < off || s[1] <= 0 || s[0] > int64(len(data)) || s[1] > int64(len(data))-s[0] {
			return nil, fmt.Errorf("native span bounds")
		}
		gap(data[off:s[0]])
		add(data[s[0] : s[0]+s[1]])
		off = s[0] + s[1]
	}
	gap(data[off:])
	return kinds.NewBundle(r, objects)
}
