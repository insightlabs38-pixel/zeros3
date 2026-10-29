package econx

import (
	"bytes"
	"math/rand"
	"reflect"
	"testing"
	"zeros3-z3/idxref"
)

func TestUnitCutAtExactLegacyCuts(t *testing.T) {
	rng := rand.New(rand.NewSource(4105))
	b := make([]byte, 512<<10)
	rng.Read(b)
	for _, n := range []int{0, 1, 4095, 4096, 4097, 65535, len(b)} {
		secs := []idxref.Sec{{Off: 0, End: n}}
		if n > 4096 {
			secs = []idxref.Sec{{Off: 0, End: 100}, {Off: 100, End: n, Items: []int{100, 101, 4095, 4096}}}
		}
		for _, target := range []int{1024, 2048, 4096} {
			a := UnitCutBytes(target)(b[:n], secs)
			c, err := UnitCutAt(bytes.NewReader(b[:n]), secs, target)
			if err != nil || !reflect.DeepEqual(a, c) {
				t.Fatalf("n=%d target=%d %v/%v %v", n, target, a, c, err)
			}
		}
	}
}
