package st

import (
	"encoding/binary"
	"encoding/json"
	"strconv"
)

// BuildTensor is one tensor handed to Build.
type BuildTensor struct {
	Name  string
	DType string
	Shape []int64
	Data  []byte
}

// KV is one __metadata__ entry.
type KV struct{ K, V string }

// Build serialises a Safetensors file in the layout the reference writer
// produces: header keys in the given order (metadata first), tensors laid out
// in that order, the header padded with spaces so that the data starts on an
// 8-byte boundary. Test and synthetic-corpus support; the caller guarantees
// Data lengths match dtype and shape.
func Build(meta []KV, ts []BuildTensor) []byte {
	var h []byte
	h = append(h, '{')
	first := true
	sep := func() {
		if !first {
			h = append(h, ',')
		}
		first = false
	}
	if len(meta) > 0 {
		sep()
		h = append(h, `"__metadata__":{`...)
		for i, kv := range meta {
			if i > 0 {
				h = append(h, ',')
			}
			k, _ := json.Marshal(kv.K)
			v, _ := json.Marshal(kv.V)
			h = append(h, k...)
			h = append(h, ':')
			h = append(h, v...)
		}
		h = append(h, '}')
	}
	var off int64
	for _, t := range ts {
		sep()
		k, _ := json.Marshal(t.Name)
		h = append(h, k...)
		h = append(h, `:{"dtype":"`...)
		h = append(h, t.DType...)
		h = append(h, `","shape":[`...)
		for i, d := range t.Shape {
			if i > 0 {
				h = append(h, ',')
			}
			h = strconv.AppendInt(h, d, 10)
		}
		h = append(h, `],"data_offsets":[`...)
		h = strconv.AppendInt(h, off, 10)
		h = append(h, ',')
		off += int64(len(t.Data))
		h = strconv.AppendInt(h, off, 10)
		h = append(h, `]}`...)
	}
	h = append(h, '}')
	for len(h)%8 != 0 {
		h = append(h, ' ')
	}
	out := binary.LittleEndian.AppendUint64(nil, uint64(len(h)))
	out = append(out, h...)
	for _, t := range ts {
		out = append(out, t.Data...)
	}
	return out
}
