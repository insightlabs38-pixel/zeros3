package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"zeros3-z3/ggufz3"
	"zeros3-z3/numcore"
	"zeros3-z3/st"
)

// mfile is an opened model file with its numcore layout (adapter from the st / ggufz3 parsers).
type mfile struct {
	path   string
	f      *os.File
	lay    *numcore.Layout
	id     numcore.ID
	format string
	byName map[string]int
	heads  [2]int // GGUF attention.head_count, head_count_kv
}

var stDT = map[string]numcore.DType{"F32": numcore.DF32, "F16": numcore.DF16, "BF16": numcore.DBF16}

func openModel(path string, hash bool) (*mfile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	m := &mfile{path: path, f: f, lay: &numcore.Layout{Size: fi.Size()}, byName: map[string]int{}}
	var head [4]byte
	f.ReadAt(head[:], 0)
	if string(head[:]) == "GGUF" {
		g, err := ggufz3.Parse(f, fi.Size(), ggufz3.Limits{})
		if err != nil {
			return nil, err
		}
		m.format = "gguf"
		for _, kv := range g.KVs {
			if strings.HasSuffix(kv.Key, "attention.head_count") {
				m.heads[0] = int(kv.Uint)
			}
			if strings.HasSuffix(kv.Key, "attention.head_count_kv") {
				m.heads[1] = int(kv.Uint)
			}
		}
		for i := range g.Tensors {
			t := &g.Tensors[i]
			nt := numcore.Tensor{Name: t.Name, DT: numcore.FromGGML(t.Type), Off: t.Begin, Len: t.Size()}
			if t.Type >= 24 && t.Type <= 28 {
				nt.DT = numcore.DRaw
			}
			if len(t.Dims) >= 2 {
				nt.Cols = int64(t.Dims[0])
			}
			m.lay.T = append(m.lay.T, nt)
		}
	} else {
		s, err := st.Parse(f, fi.Size(), st.Options{AllowUnknownDType: true})
		if err != nil {
			return nil, err
		}
		m.format = "st"
		for i := range s.Tensors {
			t := &s.Tensors[i]
			nt := numcore.Tensor{Name: t.Name, DT: stDT[t.DType], Off: s.DataStart + t.Begin, Len: t.Size()}
			if len(t.Shape) >= 2 {
				nt.Cols = t.Shape[len(t.Shape)-1]
			}
			m.lay.T = append(m.lay.T, nt)
		}
	}
	sort.Slice(m.lay.T, func(a, b int) bool { return m.lay.T[a].Off < m.lay.T[b].Off })
	for i := range m.lay.T {
		m.byName[m.lay.T[i].Name] = i
	}
	if err := m.lay.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if hash {
		h := sha256.New()
		if _, err := io.Copy(h, io.NewSectionReader(f, 0, fi.Size())); err != nil {
			return nil, err
		}
		h.Sum(m.id[:0])
	}
	return m, nil
}

func (m *mfile) src() numcore.Source { return numcore.RawSource{R: m.f, L: m.lay} }

var hfName = map[string]string{"attn_norm": "input_layernorm.weight", "ffn_norm": "post_attention_layernorm.weight",
	"attn_q": "self_attn.q_proj.weight", "attn_k": "self_attn.k_proj.weight", "attn_v": "self_attn.v_proj.weight", "attn_output": "self_attn.o_proj.weight",
	"ffn_gate": "mlp.gate_proj.weight", "ffn_up": "mlp.up_proj.weight", "ffn_down": "mlp.down_proj.weight"}

var blkRe = regexp.MustCompile(`^blk\.(\d+)\.(\w+)\.weight$`)

// hfOf maps a llama.cpp GGUF tensor name to the Hugging Face name (llama-style architectures).
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

func printRSS(label string) {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Fprintf(os.Stderr, "{\"rss\":%q,\"maxrss_kib\":%d,\"heap_sys_mib\":%d,\"total_alloc_mib\":%d,\"user_s\":%.2f,\"sys_s\":%.2f}\n", label, ru.Maxrss, ms.HeapSys>>20, ms.TotalAlloc>>20,
		float64(ru.Utime.Sec)+float64(ru.Utime.Usec)/1e6, float64(ru.Stime.Sec)+float64(ru.Stime.Usec)/1e6)
}
