package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A killed fork client can leave an identical commit in flight at its server.
// Reproduce the interleaving deterministically, without sleeps or a weaker test.
func TestA4QualificationForkCommitRace(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		createOnly, other, changeAgain bool
		wantConflict                   bool
	}{
		{"equivalent", true, false, false, false},
		{"different", true, true, false, true},
		{"changes-before-retry", true, false, true, true},
		{"ordinary-replication", false, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, cfgFor, closeFn := newForkTestServer(t, "src", "dst")
			defer closeFn()
			data := []byte("captured source revision")
			putObj(t, srv, "src", "k", data)
			src, dst := cfgFor("src"), cfgFor("dst")
			src.Key = "k"
			dst.Key = "k"
			p, e := planReplication(replicateConfig{Source: src, Dest: dst, DestMustBeAbsent: tc.createOnly})
			if e != nil {
				t.Fatal(e)
			}
			landed := data
			if tc.other {
				landed = []byte("other writer")
			}
			putObj(t, srv, "dst", "k", landed)
			posts := 0
			wrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == zeros3SyncCommitPath {
					posts++
					if posts == 2 && tc.changeAgain {
						putObj(t, srv, "dst", "k", []byte("later writer"))
					}
				}
				srv.ServeHTTP(w, r)
			}))
			defer wrapper.Close()
			p.cfg.Dest.Endpoint = wrapper.URL
			p.cfg.Dest.HTTPClient = wrapper.Client()
			_, e = executeReplicationPlan(p)
			if tc.changeAgain && posts != 2 {
				t.Fatal("retry race was not exercised")
			}
			if errors.Is(e, errSyncRemoteConflict) != tc.wantConflict || (e != nil && !tc.wantConflict) {
				t.Fatalf("conflict=%v, want %v: %v", errors.Is(e, errSyncRemoteConflict), tc.wantConflict, e)
			}
			_, got, e := srv.store.GetObject("dst", "k")
			if e != nil {
				t.Fatal(e)
			}
			want := landed
			if tc.changeAgain && posts == 2 {
				want = []byte("later writer")
			}
			if !bytes.Equal(got, want) {
				t.Fatal("racing destination overwritten")
			}
		})
	}
}
