package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"zeros3-z3/ggufz3"
	"zeros3-z3/qcore"
)

func init() { cmds["quantconf"] = cmdQuantConf }

type gg struct {
	f    *os.File
	g    *ggufz3.File
	name map[string]*ggufz3.Tensor
}

func openGG(path string) *gg {
	f, err := os.Open(path)
	must(err)
	st, err := f.Stat()
	must(err)
	g, err := ggufz3.Parse(f, st.Size(), ggufz3.Limits{})
	must(err)
	x := &gg{f: f, g: g, name: map[string]*ggufz3.Tensor{}}
	for i := range g.Tensors {
		x.name[g.Tensors[i].Name] = &g.Tensors[i]
	}
	return x
}

func (x *gg) read(t *ggufz3.Tensor) []byte {
	b := make([]byte, t.Size())
	_, err := x.f.ReadAt(b, t.Begin)
	must(err)
	return b
}

// floats decodes an F32/F16/BF16 GGUF tensor to float32 (exact widening).
func floats(typ uint32, b []byte) ([]float32, bool) {
	switch typ {
	case 0:
		out := make([]float32, len(b)/4)
		for i := range out {
			out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
		}
		return out, true
	case 1:
		out := make([]float32, len(b)/2)
		for i := range out {
			out[i] = qcore.F16to32(binary.LittleEndian.Uint16(b[2*i:]))
		}
		return out, true
	case 30:
		out := make([]float32, len(b)/2)
		for i := range out {
			out[i] = math.Float32frombits(uint32(binary.LittleEndian.Uint16(b[2*i:])) << 16)
		}
		return out, true
	}
	return nil, false
}

type confRow struct {
	File     string `json:"file"`
	Type     string `json:"type"`
	Variant  string `json:"variant"` // ref | imatrix
	Tensors  int    `json:"tensors"`
	Blocks   int64  `json:"blocks"`
	Exact    int64  `json:"exact_blocks"`
	ExactT   int    `json:"exact_tensors"`
	Bytes    int64  `json:"bytes"`
	Unported int    `json:"unported_tensors,omitempty"`
}

// quantconf -src FLOAT.gguf [-imatrix FILE] [-json OUT] TARGET.gguf...
// For every quantized tensor of every target: requantize the same-named float tensor with the
// reference and (if the imatrix has an entry) the importance-weighted quantizer; count
// byte-identical blocks against the producer's bytes.
func cmdQuantConf(args []string) {
	fl := flag.NewFlagSet("quantconf", flag.ExitOnError)
	srcP := fl.String("src", "", "float GGUF (F32/F16/BF16) with the same tensor names")
	imP := fl.String("imatrix", "", "llama.cpp imatrix file")
	jsonP := fl.String("json", "", "append JSONL rows here")
	workers := fl.Int("workers", 4, "parallel tensors")
	fl.Parse(args)
	src := openGG(*srcP)
	var im *qcore.IMatrix
	if *imP != "" {
		b, err := os.ReadFile(*imP)
		must(err)
		im, err = qcore.ParseIMatrix(b)
		must(err)
	}
	var out *os.File
	if *jsonP != "" {
		var err error
		out, err = os.OpenFile(*jsonP, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		must(err)
		defer out.Close()
	}
	for _, tp := range fl.Args() {
		tg := openGG(tp)
		rows := map[string]*confRow{}
		get := func(typ, variant string) *confRow {
			k := typ + "/" + variant
			if rows[k] == nil {
				rows[k] = &confRow{File: filepath.Base(filepath.Dir(tp)) + "/" + filepath.Base(tp), Type: typ, Variant: variant}
			}
			return rows[k]
		}
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, *workers)
		for i := range tg.g.Tensors {
			t := &tg.g.Tensors[i]
			if t.Type == 0 || t.Type == 1 || t.Type == 30 {
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				qt := qcore.Type(t.Type)
				s := src.name[t.Name]
				if !qcore.Supported(qt) || s == nil || len(t.Dims) == 0 {
					mu.Lock()
					r := get(ggufz3.Geom[t.Type].Name, "unported")
					r.Unported++
					r.Bytes += t.Size()
					mu.Unlock()
					return
				}
				x, ok := floats(s.Type, src.read(s))
				if !ok {
					return
				}
				npr := int(t.Dims[0])
				want := tg.read(t)
				_, bs := qcore.Block(qt)
				type variant struct {
					name string
					qw   []float32
					ar   qcore.Arith
				}
				variants := []variant{{"ref", nil, qcore.Strict}}
				if w := im.For(t.Name, npr); w != nil {
					variants = append(variants, variant{"imatrix", w, qcore.Strict}, variant{"imatrix+fma", w, qcore.GCCFMA})
				}
				for _, v := range variants {
					got := make([]byte, len(want))
					if err := qcore.Quantize(got, qt, x, npr, v.qw, v.ar); err == qcore.ErrArith {
						continue
					} else if err != nil {
						must(fmt.Errorf("%s: %w", t.Name, err))
					}
					eq := int64(0)
					for b := 0; b+bs <= len(want); b += bs {
						if bytes.Equal(got[b:b+bs], want[b:b+bs]) {
							eq++
						}
					}
					mu.Lock()
					r := get(qt.String(), v.name)
					r.Tensors++
					r.Bytes += t.Size()
					r.Blocks += int64(len(want) / bs)
					r.Exact += eq
					if eq == int64(len(want)/bs) {
						r.ExactT++
					}
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		keys := make([]string, 0, len(rows))
		for k := range rows {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			r := rows[k]
			fmt.Printf("%-22s %-7s %-11s tensors %3d exact-tensors %3d blocks %8d exact %8d bytes %d\n", r.File, r.Type, r.Variant, r.Tensors, r.ExactT, r.Blocks, r.Exact, r.Bytes)
			if out != nil {
				j, _ := json.Marshal(r)
				out.Write(append(j, '\n'))
			}
		}
		tg.f.Close()
	}
}
