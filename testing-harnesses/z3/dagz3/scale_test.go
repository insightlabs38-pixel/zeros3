package dagz3

import "testing"

func TestWideAndFanIn(t *testing.T) {
	for _, n := range []int{100, 1000} {
		w, err := MeasureWide(n)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("wide %d: %d objects, root %d B, reach %v, reconstruct %v, %d reads", n, w.Objects, w.RootBytes, w.Reach, w.Reconstruct, w.ReachReads)
		if w.Objects != 2*n+1 || w.ReachReads != n+1 {
			t.Fatalf("objects %d reads %d", w.Objects, w.ReachReads)
		}
		f, err := MeasureFanIn(n)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("fan-in %d: %d objects, union walk %v (%d reads), separate walks %v (%d reads)", n, f.Objects, f.Reach, f.ReachReads, f.SumReach, f.SumReads)
		if f.ReachReads >= f.SumReads {
			t.Fatalf("union walk reads %d, separate walks %d", f.ReachReads, f.SumReads)
		}
	}
}
