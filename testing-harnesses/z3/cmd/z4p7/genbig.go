package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
)

func init() { cmds["gen-big"] = cmdGenBig }

// gen-big OUTDIR MIB: SYNTHETIC_CONTROL for scale tests. Writes OUTDIR/a.safetensors (F32 tensors of
// 16 Mi elements holding BF16-representable weights, MIB mebibytes in total) and b.safetensors (the
// same with a quarter of the elements moved by a few steps), streaming, without holding a tensor.
func cmdGenBig(args []string) {
	dir := args[0]
	mib, _ := strconv.Atoi(args[1])
	must(os.MkdirAll(dir, 0o755))
	const elems = 16 << 20
	nt := mib * (1 << 20) / (4 * elems)
	hdr := map[string]any{}
	for i := 0; i < nt; i++ {
		hdr[fmt.Sprintf("layer.%03d.weight", i)] = map[string]any{"dtype": "F32", "shape": []int{4096, elems / 4096}, "data_offsets": []int64{int64(i) * 4 * elems, int64(i+1) * 4 * elems}}
	}
	hb, _ := json.Marshal(hdr)
	for len(hb)%8 != 0 {
		hb = append(hb, ' ')
	}
	for _, name := range []string{"a", "b"} {
		f, err := os.Create(filepath.Join(dir, name+".safetensors"))
		must(err)
		w := bufio.NewWriterSize(f, 4<<20)
		var l [8]byte
		binary.LittleEndian.PutUint64(l[:], uint64(len(hb)))
		w.Write(l[:])
		w.Write(hb)
		r := rand.New(rand.NewPCG(11, 12)) // same stream for both files
		p := rand.New(rand.NewPCG(13, 14))
		var b [4]byte
		for i := 0; i < nt*elems; i++ {
			v := math.Float32bits(float32(r.NormFloat64()*0.03)) &^ 0xffff
			if name == "b" && p.IntN(4) == 0 {
				v += uint32(p.IntN(5)-2) << 16
			}
			binary.LittleEndian.PutUint32(b[:], v)
			w.Write(b[:])
		}
		must(w.Flush())
		must(f.Close())
	}
	fmt.Printf("wrote %d tensors x %d elements (%d MiB per file)\n", nt, elems, nt*4*elems>>20)
}
