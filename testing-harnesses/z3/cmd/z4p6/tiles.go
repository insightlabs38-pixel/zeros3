package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"

	"zeros3-z3/semcas"
)

// cmdTiles prices the layout trade-off for transposed ranges. One BF16 matrix [R,C] is stored
// three ways and 4 KiB ranges of its row-major and transposed views are read through verified
// 4 KiB blocks:
//
//	rowmajor : raw row-major root, transposed view derived (the B3 layout relation)
//	tiled    : raw tile-major root (TxT tiles), both views derived by one exact permutation
//	hot      : both views stored raw (2x bytes)
func cmdTiles(args []string) {
	fl := flag.NewFlagSet("tiles", flag.ExitOnError)
	rows := fl.Int("rows", 1536, "matrix rows")
	cols := fl.Int("cols", 576, "matrix cols")
	tile := fl.Int("tile", 32, "tile edge (elements)")
	n := fl.Int("n", 200, "requests per view")
	block := fl.Int("block", 4096, "verified block size")
	fl.Parse(args)
	R, C, T := *rows, *cols, *tile
	if R%T != 0 || C%T != 0 {
		must(fmt.Errorf("rows and cols must be multiples of the tile edge"))
	}
	rng := rand.New(rand.NewSource(3))
	row := make([]byte, 2*R*C)
	rng.Read(row)
	rowD := semcas.Desc{Kind: semcas.KindTensor, Version: 1, DType: semcas.DBF16, Shape: []uint64{uint64(R), uint64(C)}, Ordered: true}
	trD := semcas.Desc{Kind: semcas.KindTensor, Version: 1, DType: semcas.DBF16, Shape: []uint64{uint64(C), uint64(R)}, Ordered: true}
	perm := func(b []byte, dims []uint64, axes []uint8) []byte {
		op := semcas.Op{Kind: semcas.OpPerm, Elem: 2, Dims: dims, Axes: axes}
		o, err := op.Apply([][]byte{b})
		must(err)
		return o
	}
	tr := perm(row, []uint64{uint64(R), uint64(C)}, []uint8{1, 0})
	tiled := perm(row, []uint64{uint64(R / T), uint64(T), uint64(C / T), uint64(T)}, []uint8{0, 2, 1, 3}) // (r1,c1,r0,c0)
	tiledD := semcas.Desc{Kind: semcas.KindTensor, Version: 1, DType: semcas.DBF16, Shape: []uint64{uint64(R / T), uint64(C / T), uint64(T), uint64(T)}, Ordered: true}

	type variant struct {
		name         string
		stored       int64
		rowCID, trID semcas.ID
		s            *semcas.Store
	}
	mk := func(name string, build func(s *semcas.Store) (semcas.ID, semcas.ID, int64)) variant {
		s, err := semcore2New()
		must(err)
		s.BlockSize = *block
		a, b, st := build(s)
		return variant{name, st, a, b, s}
	}
	vs := []variant{
		mk("rowmajor", func(s *semcas.Store) (semcas.ID, semcas.ID, int64) {
			r, err := s.PutRaw(rowD, row)
			must(err)
			d, err := s.PutDerived(trD, []semcas.ID{r.CID}, []semcas.Op{{Kind: semcas.OpPerm, Elem: 2, Dims: []uint64{uint64(R), uint64(C)}, Axes: []uint8{1, 0}}}, nil, tr)
			must(err)
			return r.CID, d.CID, int64(len(row))
		}),
		mk("tiled", func(s *semcas.Store) (semcas.ID, semcas.ID, int64) {
			root, err := s.PutRaw(tiledD, tiled)
			must(err)
			dims := []uint64{uint64(R / T), uint64(C / T), uint64(T), uint64(T)}
			rm, err := s.PutDerived(rowD, []semcas.ID{root.CID}, []semcas.Op{{Kind: semcas.OpPerm, Elem: 2, Dims: dims, Axes: []uint8{0, 2, 1, 3}}}, nil, row)
			must(err)
			tv, err := s.PutDerived(trD, []semcas.ID{root.CID}, []semcas.Op{{Kind: semcas.OpPerm, Elem: 2, Dims: dims, Axes: []uint8{1, 3, 0, 2}}}, nil, tr)
			must(err)
			return rm.CID, tv.CID, int64(len(tiled))
		}),
		mk("hot", func(s *semcas.Store) (semcas.ID, semcas.ID, int64) {
			a, err := s.PutRaw(rowD, row)
			must(err)
			b, err := s.PutRaw(trD, tr)
			must(err)
			return a.CID, b.CID, int64(len(row) + len(tr))
		}),
	}
	var out []map[string]any
	for _, v := range vs {
		for _, view := range []struct {
			name string
			cid  semcas.ID
			want []byte
		}{{"rowmajor_view", v.rowCID, row}, {"transposed_view", v.trID, tr}} {
			rc := &semcas.Recipe{Size: int64(len(view.want)), SHA: semcas.PayloadSHA(view.want), Steps: []semcas.Step{{Kind: semcas.StepRef, CID: view.cid}}}
			var warm semcas.IO
			if _, err := v.s.ReadRange(rc, 0, 1, &warm); err != nil { // first touch outside the measurement
				must(err)
			}
			var bytesRead, blocks int64
			for i := 0; i < *n; i++ {
				off := rng.Int63n(int64(len(view.want) - 4096))
				var io semcas.IO
				got, err := v.s.ReadRange(rc, off, 4096, &io)
				must(err)
				if !bytes.Equal(got, view.want[off:off+4096]) {
					must(fmt.Errorf("%s/%s: wrong bytes at %d", v.name, view.name, off))
				}
				bytesRead += io.Bytes
				blocks += int64(io.BlocksRead)
			}
			out = append(out, map[string]any{"layout": v.name, "view": view.name, "stored_payload_bytes": v.stored, "block": *block,
				"avg_bytes_read_4k": bytesRead / int64(*n), "avg_blocks": blocks / int64(*n), "amplification": float64(bytesRead) / float64(*n) / 4096})
		}
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"rows": R, "cols": C, "tile": T, "results": out})
}

// semcore2New is an in-memory store with the research format extensions (tests/experiments).
func semcore2New() (*semcas.Store, error) {
	s, err := semcas.NewStore(semcas.NewMemBackend())
	if err == nil {
		s.Ext = semcas.FormatExt{}
	}
	return s, err
}
