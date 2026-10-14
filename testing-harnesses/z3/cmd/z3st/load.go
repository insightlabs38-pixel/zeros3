package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"

	"zeros3-z3/corpus"
	"zeros3-z3/st"
)

// ckpt is one parsed checkpoint file held in memory.
type ckpt struct {
	label string
	path  string
	data  []byte
	f     *st.File
	hash  map[string][32]byte
}

func (c *ckpt) payload(t *st.Tensor) []byte {
	return c.data[c.f.DataStart+t.Begin : c.f.DataStart+t.End]
}

func (c *ckpt) hashes() map[string][32]byte {
	if c.hash == nil {
		c.hash = make(map[string][32]byte, len(c.f.Tensors))
		for i := range c.f.Tensors {
			t := &c.f.Tensors[i]
			c.hash[t.Name] = sha256.Sum256(c.payload(t))
		}
	}
	return c.hash
}

// load opens every revision of each selected family (in manifest order).
func load(dir string, families []string) (map[string][]*ckpt, []string) {
	m, err := corpus.ReadManifest(dir)
	if err != nil {
		fatal(err)
	}
	names, by := m.Group()
	out := map[string][]*ckpt{}
	var order []string
	for _, fam := range names {
		if !matchAny(families, fam) || (len(by[fam]) > 0 && by[fam][0].Format != "safetensors") {
			continue
		}
		for _, mem := range by[fam] {
			p, err := corpus.MemberPath(dir, mem)
			if err != nil {
				fatal(err)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				fatal(err)
			}
			f, err := st.ParseBytes(b, st.Options{})
			if err != nil {
				fatal(fmt.Errorf("%s: %w", p, err))
			}
			label := mem.Mutation
			if label == "" {
				label = mem.Revision
			}
			out[fam] = append(out[fam], &ckpt{label: label, path: p, data: b, f: f})
		}
		order = append(order, fam)
	}
	return out, order
}

// tensorClass names a tensor by generic name patterns (analysis only).
func tensorClass(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "embed_in") || strings.Contains(n, "embed_tokens") || strings.Contains(n, "word_embeddings") || strings.Contains(n, "wte") || strings.Contains(n, "wpe") || strings.Contains(n, "position_embeddings") || strings.Contains(n, "token_type_embeddings"):
		return "embedding"
	case strings.Contains(n, "embed_out") || strings.Contains(n, "lm_head") || strings.Contains(n, "classifier") || strings.Contains(n, "cls.") || strings.Contains(n, "qa_outputs") || strings.Contains(n, "score."):
		return "output head"
	case strings.Contains(n, "layernorm") || strings.Contains(n, "layer_norm") || strings.Contains(n, "ln_") || strings.Contains(n, ".norm") || strings.Contains(n, "norm."):
		return "normalization"
	case strings.Contains(n, "attention.query_key_value") || strings.Contains(n, "attn") || strings.Contains(n, "attention.self") || strings.Contains(n, "attention.dense") || strings.Contains(n, "attention.output") || strings.Contains(n, "self_attn") || strings.Contains(n, "query") || strings.Contains(n, ".key") || strings.Contains(n, "value"):
		return "attention"
	case strings.Contains(n, "mlp") || strings.Contains(n, "dense_h_to_4h") || strings.Contains(n, "dense_4h_to_h") || strings.Contains(n, "intermediate") || strings.Contains(n, "ffn") || strings.Contains(n, "output.dense"):
		return "mlp"
	case strings.HasSuffix(n, ".bias") || strings.HasSuffix(n, "bias"):
		return "other bias"
	case strings.Contains(n, "rotary") || strings.Contains(n, "inv_freq") || strings.Contains(n, "masked_bias") || strings.Contains(n, "position_ids") || strings.Contains(n, "attention.bias"):
		return "buffer (non-trainable)"
	}
	return "other"
}
