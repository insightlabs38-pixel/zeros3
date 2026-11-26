package main

// FB-02 M-A6: per-restore-session snapshot index (snapshotSessionTable).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fb02SnapshotDecodes counts full snapshot reads while it is installed.
func fb02SnapshotDecodes(t *testing.T) *atomic.Int64 {
	var n atomic.Int64
	withTestHook(t, func(point string) {
		if point == hookSnapshotDecoded {
			n.Add(1)
		}
	})
	return &n
}

func fb02SnapshotFixture(t *testing.T, n int) (*Server, string, syncClientConfig, string, func()) {
	t.Helper()
	srv, dir, cfg, closeFn := newSnapshotTestServer(t, "b")
	for i := 0; i < n; i++ {
		body := genRandomBytes(int64(9000+i), 2000+i)
		meta := map[string]string{"i": fmt.Sprint(i)}
		if _, err := srv.store.PutObject("b", fmt.Sprintf("src/k%04d", i), body, "text/plain", meta); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := createSnapshotRemote(cfg, "b", "src")
	if err != nil {
		t.Fatal(err)
	}
	return srv, dir, cfg, snap.SnapshotID, closeFn
}

func fb02SnapshotGet(t *testing.T, cfg syncClientConfig, id, key, session string) (int, []byte) {
	t.Helper()
	q := url.Values{"id": {id}, "key": {key}}
	if session != "" {
		q.Set("session", session)
	}
	resp, body, err := cfg.signAndDo(context.Background(), http.MethodGet, zeros3SnapshotObjectPath+"?"+q.Encode(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func fb02OpenSession(t *testing.T, cfg syncClientConfig, id string) (int, snapshotSessionResponse) {
	t.Helper()
	resp, body, err := cfg.signAndDo(context.Background(), http.MethodPost, zeros3SnapshotSessionPath+"?"+url.Values{"id": {id}}.Encode(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var r snapshotSessionResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &r); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, r
}

func fb02Sessions(srv *Server) int {
	srv.snapSessions.mu.Lock()
	defer srv.snapSessions.mu.Unlock()
	return len(srv.snapSessions.sessions)
}

// Every describe answered from a session is byte-identical to the fresh,
// full-read answer, for present keys, absent keys and keys between entries.
func TestFB02_SnapshotSessionDescribeEqualsFresh(t *testing.T) {
	_, _, cfg, id, closeFn := fb02SnapshotFixture(t, 40)
	defer closeFn()
	code, open := fb02OpenSession(t, cfg, id)
	if code != 200 || open.Session == "" || open.Entries != 40 {
		t.Fatalf("open: %d %+v", code, open)
	}
	keys := []string{"src/k0000", "src/k0017", "src/k0039", "src/k00", "src/k0040", "src/", "a", "zzz"}
	for _, k := range keys {
		fc, fb := fb02SnapshotGet(t, cfg, id, k, "")
		sc, sb := fb02SnapshotGet(t, cfg, id, k, open.Session)
		if fc != sc || !bytes.Equal(fb, sb) {
			t.Fatalf("key %q: fresh %d %s, session %d %s", k, fc, fb, sc, sb)
		}
		wantFound := strings.HasPrefix(k, "src/k00") && len(k) == len("src/k0000") && k != "src/k0040"
		if (fc == 200) != wantFound {
			t.Fatalf("key %q: status %d", k, fc)
		}
	}
	// A token for this snapshot says nothing about another id, and an
	// unknown token is a full read: both still answer correctly.
	if c, _ := fb02SnapshotGet(t, cfg, newUUIDv7(), "src/k0000", open.Session); c != http.StatusNotFound {
		t.Fatalf("session used with another snapshot id: %d", c)
	}
	if c, b := fb02SnapshotGet(t, cfg, id, "src/k0001", "not-a-session"); c != 200 || !bytes.Contains(b, []byte(`"src/k0001"`)) {
		t.Fatalf("unknown session token: %d %s", c, b)
	}
}

// A restore decodes the snapshot twice (show, then the session), not once
// per object, and lands exactly what a sessionless restore lands.
func TestFB02_SnapshotSessionRestoreDecodesOnce(t *testing.T) {
	srv, _, cfg, id, closeFn := fb02SnapshotFixture(t, 30)
	defer closeFn()
	dest := cfg
	dest.Bucket = "b"
	decodes := fb02SnapshotDecodes(t)
	res, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg, SnapshotID: id, Dest: dest, DestPrefix: "sess"})
	if err != nil || !res.OK() || res.Replicated != 30 {
		t.Fatalf("session restore: %+v %v", res, err)
	}
	if got := decodes.Load(); got != 2 {
		t.Fatalf("session restore of 30 objects decoded the snapshot %d times, want 2", got)
	}
	if n := fb02Sessions(srv); n != 0 {
		t.Fatalf("restore left %d sessions open", n)
	}
	// Without sessions every describe reads the snapshot again.
	table := srv.snapSessions
	srv.snapSessions = nil
	decodes.Store(0)
	res, err = restoreNamespace(restoreNamespaceConfig{Snapshot: cfg, SnapshotID: id, Dest: dest, DestPrefix: "fresh"})
	srv.snapSessions = table
	if err != nil || !res.OK() || res.Replicated != 30 {
		t.Fatalf("fresh restore: %+v %v", res, err)
	}
	if got := decodes.Load(); got != 31 {
		t.Fatalf("fresh restore of 30 objects decoded the snapshot %d times, want 31", got)
	}
	for i := 0; i < 30; i++ {
		a, am, _ := srv.store.headServed("b", fmt.Sprintf("sess/k%04d", i))
		b, bm, _ := srv.store.headServed("b", fmt.Sprintf("fresh/k%04d", i))
		if a == nil || b == nil || a.etag != b.etag || a.size != b.size || am.man.ObjectSHA256 != bm.man.ObjectSHA256 {
			t.Fatalf("object %d differs between the session and fresh restores", i)
		}
	}
}

// A snapshot deleted while a session is open: the next describe is
// NoSuchSnapshot, the session is gone, and a restore under way records every
// later object as failed, exactly as a sessionless restore does.
func TestFB02_SnapshotSessionDeletedMidRestore(t *testing.T) {
	for _, sessions := range []bool{true, false} {
		t.Run(fmt.Sprintf("sessions=%v", sessions), func(t *testing.T) {
			srv, _, cfg, id, closeFn := fb02SnapshotFixture(t, 12)
			defer closeFn()
			if !sessions {
				srv.snapSessions = nil
			}
			dest := cfg
			dest.Bucket = "b"
			var acks atomic.Int64
			withTestHook(t, func(point string) {
				if point == hookAfterAck && acks.Add(1) == 5 {
					if err := srv.store.deleteSnapshot(id); err != nil {
						t.Errorf("delete: %v", err)
					}
				}
			})
			res, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg, SnapshotID: id, Dest: dest, DestPrefix: "out"})
			if err != nil || res.Replicated != 5 || res.Failed != 7 {
				t.Fatalf("restore with the snapshot deleted after 5 objects: %+v %v", res, err)
			}
			for _, f := range res.Failures {
				if !strings.Contains(f.Err.Error(), "404") {
					t.Fatalf("failure %s: %v", f.Key, f.Err)
				}
			}
			if sessions && fb02Sessions(srv) != 0 {
				t.Fatalf("a deleted snapshot's session survived")
			}
		})
	}
}

// Corruption before the session opens refuses the session; corruption after
// it opens drops the session at the next describe, and the full read
// reports SnapshotCorrupt, as a sessionless describe does.
func TestFB02_SnapshotSessionCorruptDescriptor(t *testing.T) {
	srv, _, cfg, id, closeFn := fb02SnapshotFixture(t, 8)
	defer closeFn()
	path := srv.store.snapshotPath(id)
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bad := bytes.Clone(good)
	bad[len(bad)/2] ^= 0x40
	write := func(b []byte, age time.Duration) {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		// Make the change visible even on a coarse-mtime filesystem.
		when := time.Now().Add(age)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}

	write(bad, time.Second)
	if c, _ := fb02OpenSession(t, cfg, id); c != http.StatusConflict {
		t.Fatalf("open over a corrupt descriptor: %d", c)
	}
	if n := fb02Sessions(srv); n != 0 {
		t.Fatalf("a corrupt descriptor opened %d sessions", n)
	}

	write(good, 2*time.Second)
	c, open := fb02OpenSession(t, cfg, id)
	if c != 200 {
		t.Fatalf("open: %d", c)
	}
	if c, _ := fb02SnapshotGet(t, cfg, id, "src/k0003", open.Session); c != 200 {
		t.Fatalf("describe in session: %d", c)
	}
	write(bad, 3*time.Second)
	sc, sb := fb02SnapshotGet(t, cfg, id, "src/k0003", open.Session)
	fc, fb := fb02SnapshotGet(t, cfg, id, "src/k0003", "")
	if sc != http.StatusConflict || fc != sc || !bytes.Equal(sb, fb) {
		t.Fatalf("describe after corruption: session %d %s, fresh %d %s", sc, sb, fc, fb)
	}
	if n := fb02Sessions(srv); n != 0 {
		t.Fatalf("a session survived its descriptor's corruption")
	}
	// A truncated (missing body) descriptor behaves the same way.
	write(good, 4*time.Second)
	_, open = fb02OpenSession(t, cfg, id)
	write(good[:len(good)/3], 5*time.Second)
	if c, _ := fb02SnapshotGet(t, cfg, id, "src/k0003", open.Session); c != http.StatusConflict {
		t.Fatalf("describe after truncation: %d", c)
	}
}

// A manifest missing from the store fails the describe identically with
// and without a session: the session holds digests, never manifests.
func TestFB02_SnapshotSessionMissingManifest(t *testing.T) {
	useLooseLayout(t, "the fixture removes one manifest file")
	srv, dir, cfg, id, closeFn := fb02SnapshotFixture(t, 4)
	defer closeFn()
	d, err := srv.store.readSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	_, open := fb02OpenSession(t, cfg, id)
	if err := os.Remove(filepath.Join(dir, "manifests", d.Entries[2].ManifestUUID+".json")); err != nil {
		t.Fatal(err)
	}
	sc, sb := fb02SnapshotGet(t, cfg, id, d.Entries[2].Key, open.Session)
	fc, fb := fb02SnapshotGet(t, cfg, id, d.Entries[2].Key, "")
	if sc == 200 || sc != fc || !bytes.Equal(sb, fb) {
		t.Fatalf("missing manifest: session %d %s, fresh %d %s", sc, sb, fc, fb)
	}
}

// The table is bounded in sessions, in index bytes and in idle time, and a
// refused session costs the restore nothing but speed.
func TestFB02_SnapshotSessionBounds(t *testing.T) {
	srv, _, cfg, id, closeFn := fb02SnapshotFixture(t, 6)
	defer closeFn()
	var tokens []string
	for i := 0; i < maxSnapshotSessions; i++ {
		c, r := fb02OpenSession(t, cfg, id)
		if c != 200 {
			t.Fatalf("open %d: %d", i, c)
		}
		tokens = append(tokens, r.Session)
	}
	if c, _ := fb02OpenSession(t, cfg, id); c != http.StatusServiceUnavailable {
		t.Fatalf("open past the session bound: %d", c)
	}
	dest := cfg
	dest.Bucket = "b"
	res, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg, SnapshotID: id, Dest: dest, DestPrefix: "full"})
	if err != nil || !res.OK() || res.Replicated != 6 {
		t.Fatalf("restore with the table full: %+v %v", res, err)
	}
	closeSnapshotSession(cfg, tokens[0])
	if c, _ := fb02OpenSession(t, cfg, id); c != 200 {
		t.Fatalf("open after a close: %d", c)
	}
	// Idle sessions expire at the next table operation.
	srv.snapSessions.mu.Lock()
	for _, ix := range srv.snapSessions.sessions {
		ix.used = ix.used.Add(-snapshotSessionIdle - time.Second)
	}
	srv.snapSessions.mu.Unlock()
	if c, _ := fb02SnapshotGet(t, cfg, id, "src/k0001", tokens[1]); c != 200 {
		t.Fatalf("describe with an expired session: %d", c)
	}
	if n := fb02Sessions(srv); n != 0 {
		t.Fatalf("%d idle sessions survived", n)
	}
	if srv.snapSessions.bytes != 0 {
		t.Fatalf("session bytes not returned: %d", srv.snapSessions.bytes)
	}
	// The byte bound.
	srv.snapSessions.mu.Lock()
	srv.snapSessions.bytes = maxSnapshotSessionBytes - 1
	srv.snapSessions.mu.Unlock()
	if c, _ := fb02OpenSession(t, cfg, id); c != http.StatusServiceUnavailable {
		t.Fatalf("open past the byte bound: %d", c)
	}
	srv.snapSessions.mu.Lock()
	srv.snapSessions.bytes = 0
	srv.snapSessions.mu.Unlock()
	// Deleting a snapshot drops its sessions at once.
	_, r := fb02OpenSession(t, cfg, id)
	if err := deleteSnapshotRemoteForTest(cfg, id); err != nil {
		t.Fatal(err)
	}
	if n := fb02Sessions(srv); n != 0 {
		t.Fatalf("snapshot delete left %d sessions", n)
	}
	if c, _ := fb02SnapshotGet(t, cfg, id, "src/k0001", r.Session); c != http.StatusNotFound {
		t.Fatalf("describe after delete: %d", c)
	}
}

// The index costs well under the 2x-payload bound the queue sets.
func TestFB02_SnapshotSessionCost(t *testing.T) {
	srv, _, cfg, id, closeFn := fb02SnapshotFixture(t, 200)
	defer closeFn()
	_, open := fb02OpenSession(t, cfg, id)
	data, err := os.ReadFile(srv.store.snapshotPath(id))
	if err != nil {
		t.Fatal(err)
	}
	cost := srv.snapSessions.bytes
	t.Logf("200 entries: descriptor file %d B, index cost %d B (%.2fx)", len(data), cost, float64(cost)/float64(len(data)))
	if cost <= 0 || cost > 2*int64(len(data)) {
		t.Fatalf("index cost %d B against a %d B descriptor", cost, len(data))
	}
	closeSnapshotSession(cfg, open.Session)
	if srv.snapSessions.bytes != 0 {
		t.Fatalf("close did not return the bytes")
	}
}

// A server that predates sessions (the endpoint unknown) or has them off
// still restores: the client falls back to a full read per describe.
func TestFB02_SnapshotSessionOldServer(t *testing.T) {
	srv, _, cfg, id, closeFn := fb02SnapshotFixture(t, 5)
	defer closeFn()
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == zeros3SnapshotSessionPath {
			http.Error(w, "no such endpoint", http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("session") != "" {
			t.Errorf("client sent a session the server never gave: %s", r.URL)
		}
		srv.ServeHTTP(w, r)
	}))
	defer old.Close()
	ocfg := cfg
	ocfg.Endpoint, ocfg.HTTPClient = old.URL, old.Client()
	dest := ocfg
	dest.Bucket = "b"
	res, err := restoreNamespace(restoreNamespaceConfig{Snapshot: ocfg, SnapshotID: id, Dest: dest, DestPrefix: "old"})
	if err != nil || !res.OK() || res.Replicated != 5 {
		t.Fatalf("restore against a server without sessions: %+v %v", res, err)
	}
}

func deleteSnapshotRemoteForTest(cfg syncClientConfig, id string) error {
	resp, body, err := cfg.signAndDo(context.Background(), http.MethodDelete, zeros3SnapshotDeletePath+"?"+url.Values{"id": {id}}.Encode(), nil, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("delete: %d %s", resp.StatusCode, body)
	}
	return nil
}
