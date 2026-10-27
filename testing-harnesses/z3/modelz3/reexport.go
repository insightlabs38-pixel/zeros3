package modelz3

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"zeros3-z3/ggufz3"
	"zeros3-z3/st"
)

// GGUFEdit describes a layout-only re-export: tensor payloads are copied byte for byte from the
// real file, only framing changes (REAL_REEXPORT, never a real model revision).
type GGUFEdit struct {
	AddKV   [][2]string // appended string key/values (header grows, every tensor offset moves)
	Reverse bool        // tensors written in reverse order
	Align   int         // 0 = keep; otherwise general.alignment is set to this
}

// RewriteGGUF writes the edited file to w.
func RewriteGGUF(r io.ReaderAt, size int64, e GGUFEdit, w io.Writer) error {
	f, err := ggufz3.Parse(r, size, ggufz3.Limits{})
	if err != nil {
		return err
	}
	hdr := make([]byte, f.InfoEnd)
	if _, err := r.ReadAt(hdr, 0); err != nil && err != io.EOF {
		return err
	}
	align := int(f.Alignment)
	if e.Align != 0 {
		align = e.Align
	}
	var h []byte
	h = append(h, "GGUF"...)
	h = binary.LittleEndian.AppendUint32(h, f.Version)
	h = binary.LittleEndian.AppendUint64(h, uint64(len(f.Tensors)))
	nkv := 0
	var kvs []byte
	for _, kv := range f.KVs {
		if e.Align != 0 && kv.Key == "general.alignment" {
			continue
		}
		kvs = append(kvs, hdr[kv.Off:kv.End]...)
		nkv++
	}
	str := func(b []byte, s string) []byte {
		return append(binary.LittleEndian.AppendUint64(b, uint64(len(s))), s...)
	}
	if e.Align != 0 {
		kvs = str(kvs, "general.alignment")
		kvs = binary.LittleEndian.AppendUint32(kvs, ggufz3.TUint32)
		kvs = binary.LittleEndian.AppendUint32(kvs, uint32(e.Align))
		nkv++
	}
	for _, kv := range e.AddKV {
		kvs = str(kvs, kv[0])
		kvs = binary.LittleEndian.AppendUint32(kvs, ggufz3.TString)
		kvs = str(kvs, kv[1])
		nkv++
	}
	h = binary.LittleEndian.AppendUint64(h, uint64(nkv))
	h = append(h, kvs...)
	order := make([]int, len(f.Tensors))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return f.Tensors[order[a]].Begin < f.Tensors[order[b]].Begin })
	if e.Reverse {
		for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
			order[i], order[j] = order[j], order[i]
		}
	}
	offs := make([]uint64, len(f.Tensors))
	var cur uint64
	for _, i := range order {
		offs[i] = cur
		cur += uint64(f.Tensors[i].Size())
		cur = (cur + uint64(align) - 1) / uint64(align) * uint64(align)
	}
	for i, t := range f.Tensors { // directory keeps the original order
		h = str(h, t.Name)
		h = binary.LittleEndian.AppendUint32(h, uint32(len(t.Dims)))
		for _, d := range t.Dims {
			h = binary.LittleEndian.AppendUint64(h, d)
		}
		h = binary.LittleEndian.AppendUint32(h, t.Type)
		h = binary.LittleEndian.AppendUint64(h, offs[i])
	}
	for len(h)%align != 0 {
		h = append(h, 0)
	}
	if _, err := w.Write(h); err != nil {
		return err
	}
	pad := make([]byte, align)
	for _, i := range order {
		t := &f.Tensors[i]
		if _, err := io.Copy(w, io.NewSectionReader(r, t.Begin, t.Size())); err != nil {
			return err
		}
		if p := (align - int(t.Size())%align) % align; p > 0 {
			if _, err := w.Write(pad[:p]); err != nil {
				return err
			}
		}
	}
	return nil
}

// STEdit describes a Safetensors layout-only re-export.
type STEdit struct {
	Reverse bool // tensors in reverse order
	Shards  int  // > 1: split into this many shards (balanced by bytes, file order)
	Meta    []st.KV
}

// RewriteST returns the re-exported file(s) (one for Shards <= 1) and, for shards, the index JSON.
func RewriteST(r io.ReaderAt, size int64, e STEdit) (files [][]byte, index []byte, err error) {
	f, err := st.Parse(r, size, st.Options{AllowUnknownDType: true})
	if err != nil {
		return nil, nil, err
	}
	ts := make([]st.BuildTensor, len(f.Tensors))
	for i := range f.Tensors {
		t := &f.Tensors[i]
		b, err := f.ReadTensor(r, t, 0, t.Size())
		if err != nil {
			return nil, nil, err
		}
		ts[i] = st.BuildTensor{Name: t.Name, DType: t.DType, Shape: t.Shape, Data: b}
	}
	if e.Reverse {
		for i, j := 0, len(ts)-1; i < j; i, j = i+1, j-1 {
			ts[i], ts[j] = ts[j], ts[i]
		}
	}
	meta := e.Meta
	if meta == nil {
		meta = []st.KV{{K: "format", V: "pt"}}
	}
	if e.Shards <= 1 {
		return [][]byte{st.Build(meta, ts)}, nil, nil
	}
	per := (f.DataLen + int64(e.Shards) - 1) / int64(e.Shards)
	var cur []st.BuildTensor
	var curN int64
	wm := map[string]string{}
	flush := func() {
		if len(cur) == 0 {
			return
		}
		name := fmt.Sprintf("model-%05d-of-%05d.safetensors", len(files)+1, e.Shards)
		for _, t := range cur {
			wm[t.Name] = name
		}
		files = append(files, st.Build(meta, cur))
		cur, curN = nil, 0
	}
	for _, t := range ts {
		if curN > 0 && curN+int64(len(t.Data)) > per && len(files) < e.Shards-1 {
			flush()
		}
		cur = append(cur, t)
		curN += int64(len(t.Data))
	}
	flush()
	// the reference index names shards by their final count
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{"metadata": map[string]any{"total_size": f.DataLen}, "weight_map": wm}); err != nil {
		return nil, nil, err
	}
	return files, buf.Bytes(), nil
}
