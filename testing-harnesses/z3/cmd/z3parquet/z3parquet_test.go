package main

import (
	"os"
	"path/filepath"
	"testing"

	"zeros3-z3/lab"
	"zeros3-z3/parquetz3"
)

func fx(t *testing.T, name string) lab.EconFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "parquetz3", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return lab.EconFile{Name: name, Data: b}
}

func cost(t *testing.T, rep repDef, store []string, target string) lab.EconAgg {
	t.Helper()
	r := rep.mk()
	st := lab.NewEconStore()
	for _, n := range store {
		st.Run(r, []lab.EconFile{fx(t, n)})
	}
	a := st.Run(r, []lab.EconFile{fx(t, target)})
	if !a.Exact && !rep.oracle {
		t.Fatalf("%s %v -> %s is not exact: %s", rep.name, store, target, a.Err)
	}
	return a
}

// The economics suite on real producer files, charged through the production pack rule and
// 96 B references: layout changes of the same table cost far less than under CDC, unrelated
// content does not false-win, and unsupported files fall back to CDC cleanly.
func TestEconomics(t *testing.T) {
	cdc, logical := fixed(cdcRep()), logicalRep("logical", parquetz3.Options{Target: 4 << 10})
	base := "pa_snappy_v1.parquet"
	for _, tc := range []struct {
		name, target string
		factor       int // logical must cost at most 1/factor of CDC
	}{
		{"row-group resize", "pa_rg500.parquet", 8},
		{"codec change snappy to zstd", "pa_zstd_v1.parquet", 4},
		{"codec change snappy to none", "pa_none_plain.parquet", 8},
		{"data page v1 to v2", "pa_snappy_v2.parquet", 8},
		{"byte-stream-split floats", "pa_bss.parquet", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := cost(t, cdc, []string{base}, tc.target)
			l := cost(t, logical, []string{base}, tc.target)
			if l.Pack*int64(tc.factor) > c.Pack {
				t.Fatalf("logical %d B vs CDC %d B (want at least %d× cheaper)", l.Pack, c.Pack, tc.factor)
			}
		})
	}
	t.Run("negative: unrelated content", func(t *testing.T) {
		first := cost(t, logical, nil, "pa_nested.parquet")
		afterOther := cost(t, logical, []string{base}, "pa_nested.parquet")
		if afterOther.Pack*100 < first.Pack*85 {
			t.Fatalf("unrelated file cost %d B after another table, %d B alone: a false win", afterOther.Pack, first.Pack)
		}
	})
	t.Run("fallback: unsupported codec", func(t *testing.T) {
		a := cost(t, logical, nil, "pa_gzip.parquet")
		if a.Fallback != 1 {
			t.Fatalf("gzip file did not fall back to CDC: %+v", a)
		}
		if c := cost(t, cdc, nil, "pa_gzip.parquet"); c.Pack != a.Pack {
			t.Fatalf("the fallback must cost exactly CDC: %d vs %d", a.Pack, c.Pack)
		}
	})
	t.Run("oracle floors the logical representation", func(t *testing.T) {
		o := cost(t, oracleValues(), []string{base}, "pa_rg500.parquet")
		l := cost(t, logical, []string{base}, "pa_rg500.parquet")
		if o.Pack > l.Pack {
			t.Fatalf("oracle %d B above the representation %d B", o.Pack, l.Pack)
		}
	})
}
