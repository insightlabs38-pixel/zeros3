package planner2

import (
	"os"
	"strings"
	"testing"
)

// FuzzLoadSeries: arbitrary bytes must never panic the cost-snapshot reader, and any
// accepted document must satisfy the cost invariants (no negative or free-base costs).
func FuzzLoadSeries(f *testing.F) {
	if b, err := os.ReadFile("../../z4/b7/series.json"); err == nil && len(b) < 1<<20 {
		f.Add(b[:min(len(b), 4000)])
	}
	f.Add([]byte(`{"Version":1,"Histories":[{"Name":"h","Stages":[{"Logical":1}],"Schemes":[{"Name":"cdc","Inc":[1],"Std":[1]}]}]}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, lins, err := LoadSeries(strings.NewReader(string(b)))
		if err != nil {
			return
		}
		for _, l := range lins {
			for ti := 0; ti < l.Len(); ti++ {
				for _, s := range l.Schemes() {
					for _, c := range l.Forms(0, ti, s.Name, nil) {
						if err := c.Validate(); err != nil {
							t.Fatalf("accepted history yields invalid candidate: %v", err)
						}
					}
				}
			}
		}
	})
}
