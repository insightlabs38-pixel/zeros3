package lab

import "testing"

func TestTarRepsExactAndSemantic(t *testing.T) {
	dir := tinyCorpus(t, "tar-meta-mtime", "tar-rename-dir", "targz-modify-one", "gzip-L6-insert-mid")
	cfg := testCfg(dir)
	cfg.Reps = []string{"cdc", "oracle-logical", "irp-tok16k", "tarmem", "tarmem-s", "tarmem-cdc16k", "oracle-tarmem", "irp-tok16k+tarmem", "irp-tok16k+tarmem-cdc64k"}
	rep, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]Row{}
	for _, r := range rep.Rows {
		rows[r.Family+"/"+r.Revision+"/"+r.Rep] = r
		if r.Status != "ok" {
			t.Fatalf("%s/%s %s: %s %s", r.Family, r.Revision, r.Rep, r.Status, r.Error)
			t.Fatalf("%s/%s %s: %s %s", r.Family, r.Revision, r.Rep, r.Status, r.Error)
		}
		if !r.Oracle && r.RoundtripExact != "yes" {
			t.Fatalf("%s/%s %s: roundtrip %s", r.Family, r.Revision, r.Rep, r.RoundtripExact)
		}
	}
	// tar structure rows exist for plain tar (exact) and are oracles over gzip
	if r := rows["tar-meta-mtime/r1/tarmem"]; r.Status != "ok" || r.Oracle {
		t.Fatalf("plain tar row: %+v", r)
	}
	if r := rows["targz-modify-one/r1/oracle-tarmem"]; r.Status != "ok" || !r.Oracle {
		t.Fatalf("gzip tar row should be an oracle: %+v", r)
	}
	if _, ok := rows["targz-modify-one/r1/tarmem"]; ok {
		t.Fatal("exact tar representation applied to a gzip file")
	}
	// member payloads are reused whatever happens to headers: mtime shift, directory rename
	for _, fam := range []string{"tar-meta-mtime", "tar-rename-dir"} {
		for _, rev := range []string{"r1", "r2"} {
			tm, cd := rows[fam+"/"+rev+"/tarmem"], rows[fam+"/"+rev+"/cdc"]
			if tm.ClassNew["tarpay"] != 0 {
				t.Errorf("%s/%s: %d payload bytes new despite unchanged members", fam, rev, tm.ClassNew["tarpay"])
			}
			if tm.NewBytes >= cd.NewBytes {
				t.Errorf("%s/%s: tarmem new %d bytes, plain CDC %d", fam, rev, tm.NewBytes, cd.NewBytes)
			}
		}
	}
	// DEFLATE + tar composes: the compressed layer is rebuilt exactly from tar components
	if r := rows["targz-modify-one/r1/irp-tok16k+tarmem"]; r.RoundtripExact != "yes" {
		t.Fatalf("composition not exact: %+v", r)
	}
	// a gzip that is not a tar fails safe: plain CDC components, still exact
	r := rows["gzip-L6-insert-mid/r1/irp-tok16k+tarmem"]
	if r.RoundtripExact != "yes" || asInt(r.Detail["tar_fallback_streams"]) < 1 {
		t.Fatalf("non-tar fallback: exact=%s detail=%v", r.RoundtripExact, r.Detail)
	}
	if r := rows["gzip-L6-insert-mid/r1/oracle-tarmem"]; r.Status == "ok" && r.Detail["tar_unsupported"] == nil {
		t.Fatalf("non-tar gzip not flagged unsupported: %+v", r)
	}
}
