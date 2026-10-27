package ggufz3

import "encoding/binary"

// BuildTensor is one tensor handed to Build (Data must be NBytes(Type, Dims) long).
type BuildTensor struct {
	Name string
	Dims []uint64
	Type uint32
	Data []byte
}

func u32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }
func u64(b []byte, v uint64) []byte { return binary.LittleEndian.AppendUint64(b, v) }
func gstr(b []byte, s string) []byte {
	return append(u64(b, uint64(len(s))), s...)
}

// Build serialises a GGUF file the way the reference writer lays it out: a metadata section with
// every value kind real files use (strings, u32/u64, f32, bool, string/f32/i32 arrays, and
// general.alignment when it is not 32), the tensor directory, padding to the alignment and the
// tensors back to back each padded to the alignment. Test and synthetic-control support only.
func Build(version uint32, align int, ts []BuildTensor) []byte {
	h := []byte("GGUF")
	h = u32(h, version)
	h = u64(h, uint64(len(ts)))
	nkv := 8
	if align != 32 {
		nkv++
	}
	h = u64(h, uint64(nkv))
	h = gstr(h, "general.architecture")
	h = u32(h, TString)
	h = gstr(h, "llama")
	h = gstr(h, "llama.block_count")
	h = u32(h, TUint32)
	h = u32(h, 12)
	h = gstr(h, "llama.rope.freq_base")
	h = u32(h, TFloat32)
	h = u32(h, 0x47c35000)
	h = gstr(h, "tokenizer.add_bos")
	h = u32(h, TBool)
	h = append(h, 1)
	h = gstr(h, "tokenizer.tokens")
	h = u32(h, TArray)
	h = u32(h, TString)
	h = u64(h, 3)
	for _, s := range []string{"<s>", "a", "bc"} {
		h = gstr(h, s)
	}
	h = gstr(h, "tokenizer.scores")
	h = u32(h, TArray)
	h = u32(h, TFloat32)
	h = u64(h, 3)
	h = append(h, make([]byte, 12)...)
	h = gstr(h, "tokenizer.token_type")
	h = u32(h, TArray)
	h = u32(h, TInt32)
	h = u64(h, 3)
	h = append(h, make([]byte, 12)...)
	h = gstr(h, "general.file_type")
	h = u32(h, TUint64)
	h = u64(h, 15)
	if align != 32 {
		h = gstr(h, "general.alignment")
		h = u32(h, TUint32)
		h = u32(h, uint32(align))
	}
	off := uint64(0)
	var data []byte
	for _, t := range ts {
		h = gstr(h, t.Name)
		h = u32(h, uint32(len(t.Dims)))
		for _, d := range t.Dims {
			h = u64(h, d)
		}
		h = u32(h, t.Type)
		h = u64(h, off)
		data = append(data, t.Data...)
		off += uint64(len(t.Data))
		for off%uint64(align) != 0 {
			data = append(data, 0)
			off++
		}
	}
	for len(h)%align != 0 {
		h = append(h, 0)
	}
	return append(h, data...)
}
