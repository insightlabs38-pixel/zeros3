package prodshadow

import "testing"

func TestGCLifecycle(t *testing.T) {
	rows, err := RunGC(bins, t.TempDir(), 300)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		t.Log(r)
		if !r.OK {
			t.Errorf("violated: %s", r)
		}
	}
}
