package main

import (
	"context"
	"crypto/sha256"
	"testing"
)

func TestC14SparseXDInNativeGraph(t *testing.T) {
	l := newAuthLab(t)
	old := make([]byte, 3<<20)
	updated := make([]byte, len(old)+xdSparsePage)
	data := genRandomBytes(14131, 320<<10)
	at := 64 << 10
	copy(old[at:], data)
	copy(updated[at+xdSparsePage:], data)
	for i := 0; i < len(data); i += 3 * xdSparsePage {
		updated[at+xdSparsePage+i+193] ^= 37
	}
	l.put("base", old)
	l.put("revision", updated)
	mustOK(t, l.s.CreateBucket("other"))
	other := append([]byte(nil), updated...)
	other[at+xdSparsePage+111] ^= 7
	_, err := l.s.PutObject("other", "same-pages", other, "application/octet-stream", nil)
	mustOK(t, err)

	ctx := context.Background()
	policy := OptimizePolicy{AuthPolicy: AuthPolicy{Order: "oldest", AllowDelta: true}, Profile: "archive", SparseFiles: true}
	dry, err := l.s.AuthorityOptimize(ctx, policy)
	mustOK(t, err)
	if dry.Discovery.SparseCandidates != 2 {
		t.Fatalf("expected only the two same-bucket directions: %+v", dry.Discovery)
	}
	policy.Bucket, policy.Apply = "b", true
	applied, err := l.s.AuthorityOptimize(ctx, policy)
	mustOK(t, err)
	if applied.Applied < 1 || applied.Planned.Deltas < 1 {
		t.Fatalf("sparse graph did not publish any delta: %+v", applied)
	}
	l.reclaim()
	l.close()
	l.open()
	l.exact("reopened sparse graph")
	l.verifyOK("reopened sparse graph")

	var rec *authRecord
	for _, content := range [][]byte{old, updated} {
		r, err := l.s.authLoad(sha256.Sum256(content))
		mustOK(t, err)
		if r != nil && r.Form == authFormDelta {
			rec = r
			break
		}
	}
	if rec == nil || len(rec.Pieces) == 0 {
		t.Fatal("missing published XD piece")
	}
	corruptChunkAnyLayout(t, l.s, rec.Pieces[0].SHA)
	l.close()
	l.open()
	verified, err := l.s.Verify(true)
	mustOK(t, err)
	if verified.OK() {
		t.Fatal("deep verification accepted a corrupted sparse delta piece")
	}
}

func TestC14SparseXDDenseAndZeroControls(t *testing.T) {
	for _, data := range [][]byte{genRandomBytes(14132, 2<<20), make([]byte, 2<<20)} {
		if features := xdSparseFeatures(data); len(features) != 0 {
			t.Fatalf("noncandidate supplied %d sparse features", len(features))
		}
		if ops, err := xdSparseOps(context.Background(), data, data); err != nil || len(ops) != 0 {
			t.Fatalf("noncandidate entered sparse matching: %v", err)
		}
	}
}
