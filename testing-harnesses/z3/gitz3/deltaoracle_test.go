package gitz3

import (
	"fmt"
	"testing"
)

func TestDeltaOracleFixtures(t *testing.T) {
	if DeltaOraclePath() == "" {
		t.Skip("gitdelta oracle not built (optional)")
	}
	o, err := StartDeltaOracle()
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	for _, a := range algos {
		for _, v := range []string{"ofs", "ref"} {
			p, _ := ParsePack(fx(t, fmt.Sprintf("%s-%s.pack", a, v)), a, Limits{})
			p.Resolve()
			st, err := DeriveDeltas(p, o)
			if err != nil {
				t.Fatal(err)
			}
			if st.Deltas == 0 || st.Exact != st.Deltas {
				t.Errorf("%v %s: %+v", a, v, st)
			}
		}
	}
}
