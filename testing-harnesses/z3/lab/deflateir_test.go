package lab

import (
	"bytes"
	"strings"
	"testing"
)

func TestDeflateIRRowsExactAndCostNA(t *testing.T) {
	cfg := testCfg(tinyCorpus(t, "deflate-L6-insert-mid", "deflate-L0-insert-mid", "deflate-huffonly-insert-mid",
		"deflate-fixed-insert-mid", "gzip-L9-insert-mid", "zip-deflate-change", "zip-stored-change", "raw-insert"))
	cfg.Reps = []string{"cdc", "deflate-ir"}
	rep, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rep.Rows {
		if r.Rep != "deflate-ir" {
			continue
		}
		n++
		if r.Format == "raw" || r.Family == "zip-stored-change" {
			t.Errorf("%s: deflate-ir applied to a format without DEFLATE streams", r.Family)
		}
		if r.Status != "ok" || r.RoundtripExact != "yes" || !r.CostNA {
			t.Errorf("%s %s: status %s exact %s costNA %v err %s", r.Family, r.Revision, r.Status, r.RoundtripExact, r.CostNA, r.Error)
		}
		if r.Incremental != 0 || r.IncrementalPacked != nil || r.VsCDCBytes != nil {
			t.Errorf("%s: a cost was invented for deflate-ir", r.Family)
		}
		if asInt(r.Detail["streams"]) == 0 || r.Detail["streams"] != r.Detail["parsed_ok"] || asInt(r.Detail["plain_bytes"]) == 0 {
			t.Errorf("%s: detail %v", r.Family, r.Detail)
		}
		if _, ok := r.Detail["replay_ms"]; !ok {
			t.Errorf("%s: no replay time", r.Family)
		}
	}
	if n == 0 {
		t.Fatal("no deflate-ir rows")
	}
	var out bytes.Buffer
	WriteTable(&out, rep, false)
	if !strings.Contains(out.String(), "deflate-ir†") || !strings.Contains(out.String(), "N/A") {
		t.Errorf("table does not mark the experimental representation:\n%s", out.String())
	}
}
