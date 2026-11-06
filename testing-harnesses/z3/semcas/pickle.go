package semcas

import (
	"encoding/binary"
	"errors"
	"math"
	"strings"
)

// TorchTensor is one tensor rebuilt in data.pkl: a view onto a storage record.
type TorchTensor struct {
	Name       string
	StorageKey string
	DType      int
	Numel      int64
	Offset     int64
	Shape      []uint64
	Stride     []int64
}

type pGlobal struct{ Mod, Name string }
type pCall struct {
	Fn   pGlobal
	Args []any
}
type pPersID struct{ ID []any }
type pMark struct{}
type pDict struct {
	Keys []any
	Vals []any
}

var errPickle = errors.New("semcas: unsupported pickle")

var torchStorageDType = map[string]int{
	"FloatStorage": DF32, "HalfStorage": DF16, "BFloat16Storage": DBF16, "DoubleStorage": DF64,
	"LongStorage": DI64, "IntStorage": DI32, "ShortStorage": DI16, "CharStorage": DI8, "ByteStorage": DU8, "BoolStorage": DBool,
}

// ScanTorchPickle interprets a torch.save data.pkl symbolically (it executes nothing and
// constructs no objects) and returns the tensors it rebuilds.
func ScanTorchPickle(b []byte) ([]TorchTensor, error) {
	if len(b) == 0 {
		return nil, errPickle
	}
	var stack []any
	var marks []int
	memo := map[uint32]any{}
	pos := 0
	need := func(n int) bool { return n >= 0 && pos+n <= len(b) }
	pop := func() (any, bool) {
		if len(stack) == 0 || (len(marks) > 0 && len(stack) <= marks[len(marks)-1]) {
			return nil, false
		}
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		return v, true
	}
	popMark := func() ([]any, bool) {
		if len(marks) == 0 {
			return nil, false
		}
		m := marks[len(marks)-1]
		marks = marks[:len(marks)-1]
		if m > len(stack) {
			return nil, false
		}
		items := append([]any(nil), stack[m:]...)
		stack = stack[:m]
		return items, true
	}
	steps := 0
	line := func() (string, bool) {
		for i := pos; i < len(b); i++ {
			if b[i] == '\n' {
				s := string(b[pos:i])
				pos = i + 1
				return s, true
			}
		}
		return "", false
	}
	for pos < len(b) {
		if steps++; steps > 1<<22 {
			return nil, errPickle
		}
		op := b[pos]
		pos++
		switch op {
		case 0x80: // PROTO
			pos++
		case 0x95: // FRAME
			pos += 8
		case '(':
			marks = append(marks, len(stack))
		case '.':
			goto done
		case 'N':
			stack = append(stack, nil)
		case 0x88:
			stack = append(stack, true)
		case 0x89:
			stack = append(stack, false)
		case 'c':
			m, ok1 := line()
			n, ok2 := line()
			if !ok1 || !ok2 {
				return nil, errPickle
			}
			stack = append(stack, pGlobal{m, n})
		case 0x93:
			n, ok1 := pop()
			m, ok2 := pop()
			ns, _ := n.(string)
			ms, _ := m.(string)
			if !ok1 || !ok2 {
				return nil, errPickle
			}
			stack = append(stack, pGlobal{ms, ns})
		case 'q':
			if !need(1) {
				return nil, errPickle
			}
			if len(stack) > 0 {
				memo[uint32(b[pos])] = stack[len(stack)-1]
			}
			pos++
		case 'r':
			if !need(4) {
				return nil, errPickle
			}
			if len(stack) > 0 {
				memo[binary.LittleEndian.Uint32(b[pos:])] = stack[len(stack)-1]
			}
			pos += 4
		case 0x94: // MEMOIZE
			if len(stack) > 0 {
				memo[uint32(len(memo))] = stack[len(stack)-1]
			}
		case 'h', 'j':
			w := 1
			if op == 'j' {
				w = 4
			}
			if !need(w) {
				return nil, errPickle
			}
			var k uint32
			if w == 1 {
				k = uint32(b[pos])
			} else {
				k = binary.LittleEndian.Uint32(b[pos:])
			}
			pos += w
			v, ok := memo[k]
			if !ok {
				return nil, errPickle
			}
			stack = append(stack, v)
		case 'K':
			if !need(1) {
				return nil, errPickle
			}
			stack = append(stack, int64(b[pos]))
			pos++
		case 'M':
			if !need(2) {
				return nil, errPickle
			}
			stack = append(stack, int64(binary.LittleEndian.Uint16(b[pos:])))
			pos += 2
		case 'J':
			if !need(4) {
				return nil, errPickle
			}
			stack = append(stack, int64(int32(binary.LittleEndian.Uint32(b[pos:]))))
			pos += 4
		case 0x8a: // LONG1
			if !need(1) || !need(1+int(b[pos])) || b[pos] > 8 {
				return nil, errPickle
			}
			n := int(b[pos])
			pos++
			var v uint64
			for i := 0; i < n; i++ {
				v |= uint64(b[pos+i]) << (8 * i)
			}
			if n > 0 && n < 8 && b[pos+n-1]&0x80 != 0 {
				v |= ^uint64(0) << (8 * n)
			}
			pos += n
			stack = append(stack, int64(v))
		case 'G':
			if !need(8) {
				return nil, errPickle
			}
			stack = append(stack, math.Float64frombits(binary.BigEndian.Uint64(b[pos:])))
			pos += 8
		case 'X', 0x8c, 'U', 'T':
			w := 4
			if op == 0x8c || op == 'U' {
				w = 1
			}
			if !need(w) {
				return nil, errPickle
			}
			var l int
			if w == 1 {
				l = int(b[pos])
			} else {
				l = int(binary.LittleEndian.Uint32(b[pos:]))
			}
			pos += w
			if !need(l) {
				return nil, errPickle
			}
			stack = append(stack, string(b[pos:pos+l]))
			pos += l
		case ')':
			stack = append(stack, []any{})
		case 0x85, 0x86, 0x87:
			n := int(op - 0x84)
			if len(stack) < n {
				return nil, errPickle
			}
			t := append([]any(nil), stack[len(stack)-n:]...)
			stack = append(stack[:len(stack)-n], t)
		case 't':
			items, ok := popMark()
			if !ok {
				return nil, errPickle
			}
			stack = append(stack, items)
		case '}':
			stack = append(stack, &pDict{})
		case ']':
			stack = append(stack, &[]any{})
		case 'a':
			v, ok1 := pop()
			l, ok2 := pop()
			lp, isl := l.(*[]any)
			if !ok1 || !ok2 || !isl {
				return nil, errPickle
			}
			*lp = append(*lp, v)
			stack = append(stack, lp)
		case 'e':
			items, ok := popMark()
			if !ok || len(stack) == 0 {
				return nil, errPickle
			}
			lp, isl := stack[len(stack)-1].(*[]any)
			if !isl {
				return nil, errPickle
			}
			*lp = append(*lp, items...)
		case 's':
			v, ok1 := pop()
			k, ok2 := pop()
			d, ok3 := pop()
			dp, isd := d.(*pDict)
			if !ok1 || !ok2 || !ok3 || !isd {
				return nil, errPickle
			}
			dp.Keys = append(dp.Keys, k)
			dp.Vals = append(dp.Vals, v)
			stack = append(stack, dp)
		case 'u':
			items, ok := popMark()
			if !ok || len(stack) == 0 || len(items)%2 != 0 {
				return nil, errPickle
			}
			dp, isd := stack[len(stack)-1].(*pDict)
			if !isd {
				return nil, errPickle
			}
			for i := 0; i < len(items); i += 2 {
				dp.Keys = append(dp.Keys, items[i])
				dp.Vals = append(dp.Vals, items[i+1])
			}
		case 'R', 0x81:
			a, ok1 := pop()
			f, ok2 := pop()
			if !ok1 || !ok2 {
				return nil, errPickle
			}
			g, _ := f.(pGlobal)
			args, _ := a.([]any)
			stack = append(stack, &pCall{g, args})
		case 'Q':
			v, ok := pop()
			t, _ := v.([]any)
			if !ok {
				return nil, errPickle
			}
			stack = append(stack, &pPersID{t})
		case 'b': // BUILD: drop state, keep object
			if _, ok := pop(); !ok {
				return nil, errPickle
			}
		case '0':
			if _, ok := pop(); !ok {
				return nil, errPickle
			}
		case '2':
			if len(stack) == 0 {
				return nil, errPickle
			}
			stack = append(stack, stack[len(stack)-1])
		default:
			return nil, errPickle
		}
		if len(stack) > 1<<20 {
			return nil, errPickle
		}
	}
	return nil, errPickle
done:
	if len(stack) == 0 {
		return nil, errPickle
	}
	var out []TorchTensor
	seen := map[*pCall]bool{}
	var walk func(v any, name string, depth int)
	walk = func(v any, name string, depth int) {
		if depth > 16 {
			return
		}
		switch x := v.(type) {
		case *pDict:
			for i := range x.Keys {
				n := name
				if s, ok := x.Keys[i].(string); ok {
					n = s
				}
				walk(x.Vals[i], n, depth+1)
			}
		case *[]any:
			for _, e := range *x {
				walk(e, name, depth+1)
			}
		case []any:
			for _, e := range x {
				walk(e, name, depth+1)
			}
		case *pCall:
			if seen[x] {
				return
			}
			seen[x] = true
			if strings.HasPrefix(x.Fn.Name, "_rebuild_tensor") && len(x.Args) >= 4 {
				if t, ok := rebuilt(x.Args, name); ok {
					out = append(out, t)
				}
				return
			}
			for _, a := range x.Args {
				walk(a, name, depth+1)
			}
		}
	}
	walk(stack[len(stack)-1], "", 0)
	return out, nil
}

func rebuilt(a []any, name string) (TorchTensor, bool) {
	var t TorchTensor
	p, ok := a[0].(*pPersID)
	if !ok || len(p.ID) < 5 {
		return t, false
	}
	if s, _ := p.ID[0].(string); s != "storage" {
		return t, false
	}
	g, _ := p.ID[1].(pGlobal)
	dt, ok := torchStorageDType[g.Name]
	if !ok {
		return t, false
	}
	key, ok1 := p.ID[2].(string)
	numel, ok2 := p.ID[4].(int64)
	off, ok3 := a[1].(int64)
	size, ok4 := ints(a[2])
	stride, ok5 := ints(a[3])
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || numel < 0 || off < 0 || len(size) != len(stride) {
		return t, false
	}
	for _, s := range size {
		if s < 0 {
			return t, false
		}
		t.Shape = append(t.Shape, uint64(s))
	}
	t.Name, t.StorageKey, t.DType, t.Numel, t.Offset, t.Stride = name, key, dt, numel, off, stride
	return t, true
}

func ints(v any) ([]int64, bool) {
	l, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]int64, len(l))
	for i, e := range l {
		x, ok := e.(int64)
		if !ok {
			return nil, false
		}
		out[i] = x
	}
	return out, true
}
