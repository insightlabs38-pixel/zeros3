package scientificz3

import (
	"bytes"
	"io"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// ReadRange reads a serialized Concat candidate, without its encoder plan.
// Model operations support partial output; compressed children rebuild their
// whole bounded original region. No whole-object logical SHA claim is made for
// a partial read; full roots are separately proved before admission.
func ReadRange(c *repz3.Call, off, n int64) ([]byte, error) {
	if c.Rep.Kind != repz3.KindConcat {
		return nil, ErrUnsupported
	}
	size := c.Rep.Size
	if size < 0 || size > MaxSource || off < 0 || off > size || n < 0 || n > 64<<20 || n > size-off {
		return nil, ErrLimit
	}
	var total int64
	for _, m := range c.Rep.Members {
		if m.Size < 0 || m.Size > MaxRegion || m.Size > MaxSource-total {
			return nil, ErrLimit
		}
		total += m.Size
	}
	if total != size {
		return nil, bad("Concat logical size")
	}
	out := make([]byte, 0, n)
	pos := int64(0)
	for i, m := range c.Rep.Members {
		end := pos + m.Size
		a, z := max(off, pos), min(off+n, end)
		if a < z {
			var data []byte
			var e error
			if m.Role == repz3.Stored {
				actual, err := c.Source().Size(m.SHA)
				if err != nil {
					return nil, err
				}
				if actual != m.Size || actual > MaxRegion {
					return nil, bad("range component size")
				}
				data, e = c.Get(m.SHA)
				if e == nil {
					data = data[a-pos : z-pos]
				}
			}
			if m.Role == repz3.Derived {
				child, err := c.Child(i)
				if err != nil {
					return nil, err
				}
				if child.Rep.Size > MaxRegion {
					return nil, ErrLimit
				}
				if child.Rep.Kind == repz3.KindModel && len(m.Frame.Prefix) == 0 && m.Frame.Zeros == 0 && child.Rep.Size == m.Size {
					verified := &verifiedSource{base: c.Source(), objects: map[repz3.SHA][]byte{}}
					env := kinds.NewEnvLimits(verified, repz3.Limits{MaxDepth: 3, MaxWhole: MaxRegion, MaxRepBytes: MaxRegion})
					authenticated, err := env.Open(m.Child)
					if err != nil {
						return nil, err
					}
					data, e = kinds.ReadModelRange(authenticated, a-pos, z-a)
				} else {
					var b bytes.Buffer
					w := &boundedWriter{w: &b, left: m.Size}
					e = c.WriteMember(i, w)
					if e == nil {
						if int64(b.Len()) != m.Size {
							return nil, bad("range derived size")
						}
						data = b.Bytes()[a-pos : z-pos]
					}
				}
			}
			if e != nil {
				return nil, e
			}
			if int64(len(data)) != z-a {
				return nil, bad("range output size")
			}
			out = append(out, data...)
		}
		pos = end
		if pos >= off+n {
			break
		}
	}
	if int64(len(out)) != n {
		return nil, bad("range coverage")
	}
	return out, nil
}

type boundedWriter struct {
	w    io.Writer
	left int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.left {
		return 0, ErrLimit
	}
	n, e := w.w.Write(p)
	w.left -= int64(n)
	if e == nil && n != len(p) {
		e = io.ErrShortWrite
	}
	return n, e
}

// The existing lab Model range primitive does not hash partial components.
// Cache the exact authenticated bytes, so the bytes verified are those served.
// This cache belongs to one synchronous range call, never a global store cache.
type verifiedSource struct {
	base    repz3.Source
	objects map[repz3.SHA][]byte
	total   int64
}

func (s *verifiedSource) Size(id repz3.SHA) (int64, error) {
	n, e := s.base.Size(id)
	if e != nil {
		return 0, e
	}
	if n < 0 || n > MaxRegion {
		return 0, ErrLimit
	}
	return n, nil
}
func (s *verifiedSource) ReadAt(id repz3.SHA, p []byte, off int64) (int, error) {
	b, ok := s.objects[id]
	if !ok {
		var e error
		b, e = repz3.ReadObject(s.base, id, -1, MaxRegion)
		if e != nil {
			return 0, e
		}
		if int64(len(b)) > MaxSource-s.total {
			return 0, ErrLimit
		}
		s.total += int64(len(b))
		s.objects[id] = b
	}
	if off < 0 || off > int64(len(b)) {
		return 0, ErrLimit
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
