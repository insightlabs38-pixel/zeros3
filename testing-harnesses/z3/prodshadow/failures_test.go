package prodshadow

import "testing"

func TestFailureMatrix(t *testing.T) {
	rows, err := RunFailureMatrix(bins, t.TempDir(), 300)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		t.Log(r)
		if !r.OK {
			t.Errorf("violated: %s", r)
		}
	}
	if len(rows) < 8 {
		t.Errorf("only %d rows", len(rows))
	}
}

func TestCatalogLoss(t *testing.T) {
	rows, err := RunCatalogLoss(bins, t.TempDir())
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
