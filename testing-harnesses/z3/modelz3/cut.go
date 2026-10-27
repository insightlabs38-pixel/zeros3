package modelz3

import (
	"fmt"
	"io"

	"zeros3-z3/lab"
)

// Cut modes. All are exact: the components concatenate to the file.
const (
	CutCDC       = "cdc"       // production CDC of the whole file (current ZeroS3)
	CutStruct    = "struct"    // one component per header / gap / tail / whole tensor
	CutStructCDC = "structcdc" // framing whole, every tensor CDC-cut on its own
	CutFixed4K   = "fixed4k"   // framing whole, every tensor in 4 KiB fixed blocks
	CutHdrCDC    = "hdrcdc"    // framing CDC-cut (it is megabytes of tokenizer in a GGUF), every tensor whole
)

// Cut splits one file into the components of the given mode.
func Cut(mode string, src *Src, lay *Layout) (CompList, error) {
	var out CompList
	var buf []byte
	whole := func(kind string, off, n int64) error {
		if n == 0 {
			return nil
		}
		id, b, err := digest(src, off, n, buf)
		buf = b
		if err != nil {
			return err
		}
		out = append(out, Comp{ID: id, Size: n, Src: src, Off: off, Kind: kind})
		return nil
	}
	cdc := func(kind string, off, n int64) error {
		return lab.ChunkCDC(io.NewSectionReader(src.R, off, n), func(o int64, c []byte) error {
			out = append(out, Comp{ID: sha256sum(c), Size: int64(len(c)), Src: src, Off: off + o, Kind: kind})
			return nil
		})
	}
	switch mode {
	case CutCDC:
		return out, cdc("chunk", 0, src.Size)
	case CutStruct, CutStructCDC, CutFixed4K, CutHdrCDC:
		for _, s := range lay.Segs {
			var err error
			switch {
			case s.Kind != "tensor" && mode == CutHdrCDC:
				err = cdc("chunk", s.Begin, s.Size())
			case s.Kind != "tensor" || mode == CutStruct || mode == CutHdrCDC:
				err = whole(s.Kind, s.Begin, s.Size())
			case mode == CutStructCDC:
				err = cdc("chunk", s.Begin, s.Size())
			default:
				for o := int64(0); o < s.Size() && err == nil; o += 4096 {
					err = whole("block", s.Begin+o, min(4096, s.Size()-o))
				}
			}
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("modelz3: unknown cut %q", mode)
}
