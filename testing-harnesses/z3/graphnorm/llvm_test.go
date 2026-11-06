package graphnorm

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func corpusBC(t *testing.T) []string {
	d := corpusDir()
	if d == "" {
		t.Skip("B4_CORPUS unset")
	}
	files, _ := filepath.Glob(filepath.Join(d, "bc", "*.bc"))
	return files
}

func TestEncodeLLVMCorpus(t *testing.T) {
	for _, f := range corpusBC(t) {
		b, _ := os.ReadFile(f)
		t0 := time.Now()
		enc, err := EncodeLLVM(b, PolicyAll)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		var tot int
		for _, p := range enc.Pieces {
			tot += len(p.Data)
		}
		t.Logf("%s: funcs=%d fallback=%d pieces=%d ops=%d sym=%d unusable=%d tmplBytes=%d idtab=%d %v kinds=%v extkeys=%v", filepath.Base(f), enc.Funcs, enc.Fallback, len(enc.Pieces), enc.Stats.Ops, enc.Stats.Sym, enc.Stats.Unusable, tot, len(enc.IDTab), time.Since(t0), enc.Stats.ByKind, enc.Stats.ExtKeys)
	}
}
