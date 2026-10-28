package admission

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"testing"
)

func TestCanonicalCapturedFrontier(t *testing.T) {
	f, e := os.Open("../../testdata/q6-admission/frontier.json.gz")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		t.Fatal(e)
	}
	defer gz.Close()
	d := json.NewDecoder(io.LimitReader(gz, 64<<20))
	d.DisallowUnknownFields()
	var c Corpus
	if e = d.Decode(&c); e != nil {
		t.Fatal(e)
	}
	if e = c.Validate(); e != nil {
		t.Fatal(e)
	}
	if len(c.Cases) != 36 || len(c.Objects) != 23887 {
		t.Fatal("canonical frontier count changed")
	}
	for _, f := range c.Cases {
		greedy, e := c.Run(f, "greedy")
		if e != nil {
			t.Fatal(e)
		}
		oracle, e := c.Run(f, "oracle")
		if e != nil {
			t.Fatal(e)
		}
		if !oracle.OracleExact || oracle.Total != oracle.Oracle || greedy.Total < oracle.Total {
			t.Fatalf("oracle/cost drift %s", f.Name)
		}
	}
}
