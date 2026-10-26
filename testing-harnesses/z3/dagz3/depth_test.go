package dagz3

import (
	"testing"

	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// kindsOf lists the kinds along the deepest path of a persisted representation.
func kindPath(t *testing.T, env *repz3.Env, id repz3.SHA) []repz3.Kind {
	t.Helper()
	rep, err := env.LoadRep(id)
	if err != nil {
		t.Fatal(err)
	}
	best := []repz3.Kind(nil)
	for _, m := range rep.Members {
		if m.Role == repz3.Derived {
			sub := kindPath(t, env, m.Child)
			if len(sub) > len(best) {
				best = sub
			}
		}
	}
	return append([]repz3.Kind{rep.Kind}, best...)
}

func TestDepth4Chains(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want []repz3.Kind
	}{
		{"A zstd>tar>gzip>sqlite", ChainA(0, 64, 4), []repz3.Kind{repz3.KindZstd, repz3.KindConcat, repz3.KindGzip, repz3.KindSQLite}},
		{"B gzip>tar>zstd>car", ChainB(0), []repz3.Kind{repz3.KindGzip, repz3.KindConcat, repz3.KindZstd, repz3.KindCAR}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := Discover(tc.data, DefaultBudget(4))
			if err != nil {
				t.Fatal(err)
			}
			if d := Depth(b); d != 4 {
				t.Fatalf("depth %d", d)
			}
			st := repz3.NewMemStore()
			id, _, err := b.Persist(st)
			if err != nil {
				t.Fatal(err)
			}
			// reload from serialized form only: no bundle, no analysis state
			env := kinds.NewEnvLimits(st, Lim)
			got := kindPath(t, env, id)
			if len(got) != 4 {
				t.Fatalf("kinds %v", got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("kinds %v, want %v", got, tc.want)
				}
			}
			if err := Exact(env, id, tc.data); err != nil {
				t.Fatal(err)
			}
		})
	}
}
