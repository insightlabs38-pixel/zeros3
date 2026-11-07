package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"runtime"
	"time"

	"zeros3-z3/mdelta2"
)

type cbRow struct {
	Coder   string  `json:"coder"`
	Bytes   int     `json:"bytes"`
	Ratio   float64 `json:"ratio_vs_payload"`
	EncMiBs float64 `json:"enc_mib_s"`
	DecMiBs float64 `json:"dec_mib_s_coder_only"`
	E2EMiBs float64 `json:"dec_mib_s_with_predict_and_reconstruct"`
	Failed  int     `json:"unsupported_tensors"`
}

// cmdCoderBench: coderbench [-maxmb N] [-json f] TARGET BASE   (residual distribution = identity key residual, XOR for PXR)
func cmdCoderBench(args []string) {
	fl := flag.NewFlagSet("coderbench", flag.ExitOnError)
	maxmb := fl.Int("maxmb", 48, "payload megabytes to use")
	jsonOut := fl.String("json", "", "output")
	label := fl.String("label", "", "distribution label")
	fl.Parse(args)
	paths := fl.Args()
	t, err := mdelta2.Open(paths[0])
	must(err)
	b, err := mdelta2.Open(paths[1])
	must(err)
	type item struct {
		dt         mdelta2.DType
		shape      []int64
		tgt, bs    []byte
		predK, pk  []byte
		resK, resX []byte
		n          int
	}
	var items []item
	var payload int
	for i := range t.Tensors {
		tt := t.Tensors[i]
		if !tt.DType.IsFloat() || tt.Elems() < 65536 || tt.Elems() > 4<<20 {
			continue
		}
		bi, ok := b.Index(tt.Name)
		if !ok || b.Tensors[bi].Elems() != tt.Elems() || !b.Tensors[bi].DType.IsFloat() {
			continue
		}
		td, _ := t.Read(i)
		bd, _ := b.Read(bi)
		dt := tt.DType
		bdt := b.Tensors[bi].DType
		if dt == mdelta2.DF32 {
			if nd, ok := mdelta2.Narrow(td, mdelta2.DF16); ok {
				td, dt = nd, mdelta2.DF16
			}
		}
		it := item{dt: dt, shape: tt.Shape, tgt: td, bs: bd, n: int(tt.Elems())}
		base := mdelta2.Base{DT: bdt, Data: bd}
		kp := &mdelta2.Params{Kind: mdelta2.KID, Res: mdelta2.RKey, NB: 1, Prec: 4}
		it.predK, _ = mdelta2.Predict(kp, dt, tt.Shape, []mdelta2.Base{base})
		it.resK = mdelta2.MakeResidual(mdelta2.RKey, dt.Width(), td, it.predK)
		it.resX = mdelta2.MakeResidual(mdelta2.RXor, dt.Width(), td, it.predK)
		items = append(items, it)
		payload += len(td)
		if payload >= *maxmb<<20 {
			break
		}
	}
	var rows []cbRow
	for c := 0; c < len(mdelta2.CoderNames); c++ {
		row := cbRow{Coder: mdelta2.CoderNames[c]}
		streams := make([][]byte, len(items))
		t0 := time.Now()
		for i, it := range items {
			res, mode := it.resK, mdelta2.RKey
			if c == mdelta2.CPXR {
				res, mode = it.resX, mdelta2.RXor
			}
			s, ok := mdelta2.EncodeResidual(c, it.dt, mode, res, it.predK)
			if !ok {
				row.Failed++
				continue
			}
			streams[i] = s
			row.Bytes += len(s)
		}
		enc := time.Since(t0)
		best := time.Hour
		for rep := 0; rep < 3; rep++ {
			runtime.GC()
			t1 := time.Now()
			for i, it := range items {
				if streams[i] == nil {
					continue
				}
				mode := mdelta2.RKey
				if c == mdelta2.CPXR {
					mode = mdelta2.RXor
				}
				if _, err := mdelta2.DecodeResidual(c, it.dt, mode, streams[i], it.predK, it.n); err != nil {
					must(fmt.Errorf("coder %s tensor %d: %w", mdelta2.CoderNames[c], i, err))
				}
			}
			if d := time.Since(t1); d < best {
				best = d
			}
		}
		e2e := time.Hour
		for rep := 0; rep < 2; rep++ {
			t1 := time.Now()
			for i, it := range items {
				if streams[i] == nil {
					continue
				}
				mode := mdelta2.RKey
				if c == mdelta2.CPXR {
					mode = mdelta2.RXor
				}
				base := mdelta2.Base{DT: b.Tensors[0].DType, Data: it.bs}
				_ = base
				kp := &mdelta2.Params{Kind: mdelta2.KID, Res: mode, NB: 1, Prec: 4}
				bdt := mdelta2.DF32
				_ = bdt
				pred, _ := mdelta2.Predict(kp, it.dt, it.shape, []mdelta2.Base{{DT: baseDT(b, it.shape, it.bs, t), Data: it.bs}})
				res, err := mdelta2.DecodeResidual(c, it.dt, mode, streams[i], pred, it.n)
				if err != nil {
					must(fmt.Errorf("e2e %s: %v", mdelta2.CoderNames[c], err))
				}
				mdelta2.ApplyResidual(mode, it.dt.Width(), res, pred)
			}
			if d := time.Since(t1); d < e2e {
				e2e = d
			}
		}
		mib := float64(payload) / (1 << 20)
		row.Ratio = float64(row.Bytes) / float64(payload)
		row.EncMiBs, row.DecMiBs, row.E2EMiBs = mib/enc.Seconds(), mib/best.Seconds(), mib/e2e.Seconds()
		rows = append(rows, row)
	}
	// reused table economics: global histogram of shifted bit-lengths vs per-tensor tables
	var perT, shared, tabBytes float64
	global := make([]float64, 96)
	hists := make([][]float64, len(items))
	for i, it := range items {
		hists[i] = mdelta2.ShiftHist(it.dt, it.resK, it.predK)
		for s, v := range hists[i] {
			global[s] += v
		}
	}
	var gN float64
	for _, v := range global {
		gN += v
	}
	for i := range items {
		var n float64
		for _, v := range hists[i] {
			n += v
		}
		for s, v := range hists[i] {
			if v > 0 {
				perT -= v * math.Log2(v/n)
				shared -= v * math.Log2(global[s]/gN)
				tabBytes += 3
			}
		}
	}
	fmt.Printf("%s: %s vs %s, %d tensors, %.1f MiB payload\n", *label, paths[0], paths[1], len(items), float64(payload)/(1<<20))
	for _, r := range rows {
		fmt.Printf("  %-13s %10d B  ratio %.4f  enc %7.1f MiB/s  dec(coder) %8.1f MiB/s  dec(end-to-end) %8.1f MiB/s  unsupported %d\n", r.Coder, r.Bytes, r.Ratio, r.EncMiBs, r.DecMiBs, r.E2EMiBs, r.Failed)
	}
	fmt.Printf("  symbol-stream entropy: per-tensor tables %.0f B (+%.0f B table bytes), one shared table %.0f B (+%d B): sharing costs %.2f%% of payload-coded bytes\n", perT/8, tabBytes, shared/8, 300, 100*((shared/8+300)-(perT/8+tabBytes))/float64(rows[2].Bytes))
	if *jsonOut != "" {
		bj, _ := json.MarshalIndent(map[string]any{"label": *label, "target": paths[0], "base": paths[1], "payload": payload, "tensors": len(items), "rows": rows, "table_per_tensor_B": perT/8 + tabBytes, "table_shared_B": shared/8 + 300}, "", " ")
		os.WriteFile(*jsonOut, bj, 0o644)
	}
}

func baseDT(b *mdelta2.Model, shape []int64, data []byte, t *mdelta2.Model) mdelta2.DType {
	for i := range b.Tensors {
		if int(b.Tensors[i].Size()) == len(data) && shapeEq(b.Tensors[i].Shape, shape) {
			return b.Tensors[i].DType
		}
	}
	return mdelta2.DF32
}

func shapeEq(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
