package prodshadow

import "testing"

func TestLifecycle(t *testing.T) {
	rows, err := RunLifecycle(bins, t.TempDir(), 1500)
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
