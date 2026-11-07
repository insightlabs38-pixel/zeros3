package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"zeros3-z3/mdelta2"
)

var hfName = map[string]string{"attn_norm": "input_layernorm.weight", "ffn_norm": "post_attention_layernorm.weight",
	"attn_q": "self_attn.q_proj.weight", "attn_k": "self_attn.k_proj.weight", "attn_v": "self_attn.v_proj.weight", "attn_output": "self_attn.o_proj.weight",
	"ffn_gate": "mlp.gate_proj.weight", "ffn_up": "mlp.up_proj.weight", "ffn_down": "mlp.down_proj.weight"}

var blkRe = regexp.MustCompile(`^blk\.(\d+)\.(\w+)\.weight$`)

func hfOf(gg string) string {
	switch gg {
	case "token_embd.weight", "output.weight":
		return "model.embed_tokens.weight"
	case "output_norm.weight":
		return "model.norm.weight"
	}
	if m := blkRe.FindStringSubmatch(gg); m != nil {
		if h, ok := hfName[m[2]]; ok {
			return "model.layers." + m[1] + "." + h
		}
	}
	return ""
}

// llamaPermute is convert_hf_to_gguf's head-interleave permutation for q/k weights.
func llamaPermute(x []float32, rows, cols, nHead int) []float32 {
	out := make([]float32, len(x))
	hd := rows / nHead / 2
	for h := 0; h < nHead; h++ {
		for a := 0; a < 2; a++ {
			for b := 0; b < hd; b++ {
				src := (h*2*hd + a*hd + b) * cols
				dst := (h*2*hd + b*2 + a) * cols
				copy(out[dst:dst+cols], x[src:src+cols])
			}
		}
	}
	return out
}

// cmdQuantRepro: quant-repro SOURCE.safetensors TARGET.gguf [nHead nKV]
func cmdQuantRepro(args []string) {
	src, err := mdelta2.Open(args[0])
	must(err)
	tg, err := mdelta2.Open(args[1])
	must(err)
	nHead, nKV := 9, 3
	if len(args) > 3 {
		nHead, _ = strconv.Atoi(args[2])
		nKV, _ = strconv.Atoi(args[3])
	}
	type agg struct{ blocks, match, bytes, matchBytes int64 }
	res := map[string]*agg{}
	for _, via := range []bool{false, true} {
		res = map[string]*agg{}
		for i := range tg.Tensors {
			t := &tg.Tensors[i]
			if !t.DType.IsQuant() {
				continue
			}
			hn := hfOf(t.Name)
			si, ok := src.Index(hn)
			if !ok {
				continue
			}
			sd, _ := src.Read(si)
			st := &src.Tensors[si]
			x := mdelta2.FloatsOf(st.DType, sd, via)
			rows, cols := st.RowsCols()
			if matched, _ := regexp.MatchString(`attn_q`, t.Name); matched {
				x = llamaPermute(x, int(rows), int(cols), nHead)
			} else if matched, _ := regexp.MatchString(`attn_k`, t.Name); matched {
				x = llamaPermute(x, int(rows), int(cols), nKV)
			}
			pred, ok := mdelta2.QuantizeAny(t.DType, x)
			if !ok {
				continue
			}
			td, _ := tg.Read(i)
			a := res[t.DType.String()]
			if a == nil {
				a = &agg{}
				res[t.DType.String()] = a
			}
			bs := int64(len(td) / (int(t.Size()) / int(blocksOf(td, t))))
			_ = bs
			g := mdelta2.BlockBytes(t.DType)
			for b := 0; b+g <= len(td); b += g {
				a.blocks++
				a.bytes += int64(g)
				eq := true
				for k := 0; k < g; k++ {
					if td[b+k] != pred[b+k] {
						eq = false
						break
					}
				}
				if eq {
					a.match++
					a.matchBytes += int64(g)
				}
			}
		}
		names := []string{}
		for k := range res {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			a := res[k]
			fmt.Printf("viaF16=%v %-5s blocks %9d exact %9d (%.4f) bytes %d\n", via, k, a.blocks, a.match, float64(a.match)/float64(a.blocks), a.bytes)
		}
	}
}

func blocksOf(b []byte, t *mdelta2.Tensor) int { return 1 }
