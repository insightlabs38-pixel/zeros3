package graphnorm

import (
	"os"
	"testing"
)

func TestDEXColumnChoices(t *testing.T) {
	d := corpusDir()
	if d == "" {
		t.Skip()
	}
	b, err := os.ReadFile(d + "/q6/fdroid-apk/2.0.1.dex")
	if err != nil {
		t.Skip()
	}
	e, err := EncodeDEX(b, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range e.Cols {
		t.Logf("%s col%s raw=%d delta=%d chosen=%d", c.Table, c.Col, c.RawBytes, c.DeltaBytes, c.Chosen)
	}
}
