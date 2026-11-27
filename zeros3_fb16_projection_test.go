package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fb16ProjectionWire struct {
	bytes, calls int64
}

func fb16ProjectionPeer(t *projectionTree, scope projectionScope, fault func(byte, []byte, []byte) []byte) (projectionPeer, *fb16ProjectionWire) {
	usage := &fb16ProjectionWire{}
	entry := &projectionCacheEntry{tree: t, token: [16]byte{1, 2, 3}, mirror: true, bucketExists: true}
	return projectionPeer{boundary: t.boundary, exchange: func(op byte, token [16]byte, data []byte) ([]byte, error) {
		if op != projectionRootRequest && token != entry.token {
			return nil, errProjectionStale
		}
		raw, err := projectionQuery(entry, op, data)
		if fault != nil && err == nil {
			raw = fault(op, data, raw)
		}
		request := projectionRequestBody(scope, op, token, data)
		usage.calls++
		usage.bytes += int64(len(request) + len(raw) + 32)
		return raw, err
	}}, usage
}

func fb16ProjectionMap(t *testing.T, values map[string][32]byte) *projectionTree {
	t.Helper()
	rows := make([]projectionRow, 0, len(values))
	for key, id := range values {
		rows = append(rows, newProjectionRow([32]byte{}, key, id))
	}
	tree, err := newProjectionTree([32]byte{}, rows)
	mustOK(t, err)
	return tree
}

func fb16ExactDiff(a, b map[string][32]byte) map[string]byte {
	out := map[string]byte{}
	for k, v := range a {
		if other, ok := b[k]; !ok {
			out[k] = 3
		} else if other != v {
			out[k] = 2
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out[k] = 1
		}
	}
	return out
}

func fb16CheckProjectionPlan(t *testing.T, p projectionPlan, want map[string]byte) {
	t.Helper()
	got := make(map[string]byte, len(p.edits))
	for i, edit := range p.edits {
		if _, found := got[edit.key]; found {
			t.Fatalf("duplicate planned edit %q", edit.key)
		}
		got[edit.key] = p.status[i]
	}
	if !maps.Equal(want, got) {
		t.Fatalf("projection diff got %d edits, exact comparison got %d", len(got), len(want))
	}
}

func TestFB16ProjectionRandomized(t *testing.T) {
	scopeA, scopeB := projectionScope{bucket: "source", prefix: "one"}, projectionScope{bucket: "dest", prefix: "another"}
	for seed := uint64(1); seed <= 36; seed++ {
		rng := rand.New(rand.NewPCG(seed, 1111))
		a, b := map[string][32]byte{}, map[string][32]byte{}
		for i := range 320 {
			if rng.IntN(7) != 0 {
				a[fmt.Sprintf("entry/%04d", i)] = sha256.Sum256(fmt.Appendf(nil, "value-%d", i))
			}
		}
		a["000-first"] = sha256.Sum256([]byte("unchanged contents"))
		a["zzz-last"] = sha256.Sum256([]byte("old last value"))
		maps.Copy(b, a)
		b["000-first"] = sha256.Sum256([]byte("metadata-only equality revision"))
		delete(b, "zzz-last")
		b["zzzz-new-last"] = sha256.Sum256([]byte("new last value"))
		for i := 0; i < 20+int(seed); i++ {
			key := fmt.Sprintf("entry/%04d", rng.IntN(380))
			if rng.IntN(4) == 0 {
				delete(b, key)
			} else {
				b[key] = sha256.Sum256(fmt.Appendf(nil, "new-%d-%d", seed, i))
			}
		}
		ta, tb := fb16ProjectionMap(t, a), fb16ProjectionMap(t, b)
		pa, _ := fb16ProjectionPeer(ta, scopeA, nil)
		pb, _ := fb16ProjectionPeer(tb, scopeB, nil)
		before := ta.root()
		plan, err := projectionCompare(&pa, &pb, true)
		mustOK(t, err)
		fb16CheckProjectionPlan(t, plan, fb16ExactDiff(a, b))
		if ta.root() != before || len(plan.guards) == 0 {
			t.Fatal("dry proof changed the tree or omitted removal guards")
		}
		leaves, _, count, err := ta.affected(plan.edits)
		mustOK(t, err)
		ta.fold(leaves, true)
		if ta.root() != tb.root() || count != len(b) {
			t.Fatal("incremental root and independently built root differ")
		}
	}
}

func TestFB16ProjectionShortHintCollision(t *testing.T) {
	ta := fb16ProjectionMap(t, map[string][32]byte{"same-key": sha256.Sum256([]byte("old"))})
	tb := fb16ProjectionMap(t, map[string][32]byte{"same-key": sha256.Sum256([]byte("new"))})
	scope := projectionScope{bucket: "b", prefix: ""}
	old := &projectionCacheEntry{tree: ta}
	a, _ := fb16ProjectionPeer(ta, scope, nil)
	b, _ := fb16ProjectionPeer(tb, scope, func(op byte, request, response []byte) []byte {
		if op == projectionBranchesRequest && len(request) >= 2 && request[1] == 4 {
			raw, err := projectionQuery(old, op, request)
			mustOK(t, err)
			return raw // force an indistinguishable short tag at every node
		}
		return response
	})
	plan, err := projectionCompare(&a, &b, false)
	mustOK(t, err)
	if !plan.strict {
		t.Fatal("short hints were trusted without verifying the full root")
	}
	fb16CheckProjectionPlan(t, plan, map[string]byte{"same-key": 2})
}

func TestFB16ProjectionWireScale(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("ZEROS3_FB16_SCALE"))
	if n == 0 {
		t.Skip("set ZEROS3_FB16_SCALE=1000000")
	}
	if n != 1_000_000 {
		t.Fatal("scale fixture expects one million rows")
	}
	start := time.Now()
	rows := make([]projectionRow, n)
	for i := range rows {
		key := fmt.Sprintf("obj/%07d.data", i)
		rows[i] = newProjectionRow([32]byte{}, key, sha256.Sum256([]byte(key)))
	}
	a, err := newProjectionTree([32]byte{}, rows)
	mustOK(t, err)
	b, err := newProjectionTree([32]byte{}, rows)
	mustOK(t, err)
	t.Logf("rows=%d setup=%s", n, time.Since(start))
	for _, change := range []int{0, 1, 1000, 100000} {
		t.Run(strconv.Itoa(change), func(t *testing.T) {
			var edits, undo []projectionEdit
			want := map[string]byte{}
			for i := range change {
				row := rows[(i*104729+31419)%n]
				edit := projectionEdit{key: row.key, sum: sha256.Sum256(row.sum[:])}
				edits, undo = append(edits, edit), append(undo, projectionEdit{key: row.key, sum: row.sum})
				want[row.key] = 2
			}
			less := func(a, b projectionEdit) int { return strings.Compare(a.key, b.key) }
			slices.SortFunc(edits, less)
			slices.SortFunc(undo, less)
			leaves, _, _, err := b.affected(edits)
			mustOK(t, err)
			b.fold(leaves, true)
			de, dWire := fb16ProjectionPeer(a, projectionScope{bucket: "to", prefix: "root"}, nil)
			src, sWire := fb16ProjectionPeer(b, projectionScope{bucket: "from", prefix: "prefix"}, nil)
			start := time.Now()
			plan, err := projectionCompare(&de, &src, false)
			mustOK(t, err)
			fb16CheckProjectionPlan(t, plan, want)
			wire := dWire.bytes + sWire.bytes
			t.Logf("differences=%d application_bytes=%d bytes_per_change=%.3f rounds=%d peer_requests=%d strict=%t elapsed=%s",
				change, wire, float64(wire)/float64(max(1, change)), plan.rounds, dWire.calls+sWire.calls, plan.strict, time.Since(start))
			switch {
			case change == 0 && plan.rounds != 1:
				t.Fatal("equal roots required traversal")
			case change > 0 && change <= 1000 && wire > 2048*int64(change):
				t.Fatalf("sparse wire gate missed: %d", wire)
			case change >= 100000 && wire > 250*int64(change):
				t.Fatalf("dense wire gate missed: %d", wire)
			}
			if plan.rounds > namespaceProjectionDepth+4 {
				t.Fatalf("overshot fixed tree descent: %d rounds", plan.rounds)
			}
			leaves, _, _, err = b.affected(undo)
			mustOK(t, err)
			b.fold(leaves, true)
			if b.root() != a.root() {
				t.Fatal("restored rows produced a different root")
			}
		})
	}
}

func fb16TestServer(t *testing.T, l *authLab, label string, available, mirror bool) (*Server, syncClientConfig) {
	t.Helper()
	credentials := Credentials{AccessKeyID: label + "-user", SecretAccessKey: label + "-local-fixture"}
	srv := NewServer(l.s, credentials, defaultRegion)
	if available {
		srv.projections = &projectionCache{}
		srv.projectionMirror = mirror
	}
	httpServer := httptest.NewServer(srv)
	t.Cleanup(httpServer.Close)
	return srv, syncClientConfig{Endpoint: httpServer.URL, Bucket: "b", Creds: credentials, Region: defaultRegion}
}

func TestFB16SignedProjectionReplication(t *testing.T) {
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	src, dst := newAuthLab(t), newAuthLab(t)
	mustOK(t, src.s.ensureFeature(journalBinFeature))
	src.s.writeBinaryJournal = true
	body := bytes.Repeat([]byte("exact source payload"), 1870)
	put := func(l *authLab, key string, body []byte, v string) {
		_, err := l.s.PutObject("b", key, body, "text/plain", map[string]string{"revision": v})
		mustOK(t, err)
	}
	put(src, "source/first", body, "new")
	put(dst, "dest/first", body, "old")
	put(src, "source/last", []byte("fresh bytes"), "new")
	put(src, "source/", []byte("directory marker"), "new")
	put(dst, "dest/orphan", []byte("retained outside source"), "original")
	put(dst, "outside/orphan", []byte("excluded from scope"), "other")
	ss, sc := fb16TestServer(t, src, "source", true, false)
	ds, dc := fb16TestServer(t, dst, "destination", true, true)
	scopeS, scopeD := projectionScope{bucket: "b", prefix: "source"}, projectionScope{bucket: "b", prefix: "dest"}
	cfg := namespaceReplicateConfig{Source: sc, Dest: dc, SourcePrefix: scopeS.prefix, DestPrefix: scopeD.prefix,
		Incremental: true, NativeBatch: true, Workers: 2}
	_, initial, err := namespaceReplicationObjects(cfg)
	mustOK(t, err)
	if !initial.active || len(initial.plan.edits) != 4 || initial.plan.strict {
		t.Fatalf("unexpected initial projection: %+v", initial.plan)
	}
	scopeS, scopeD = projectionReplicationScopes(cfg)
	sourceTree := ss.projections.entries[scopeS].tree
	destTree := ds.projections.entries[scopeD].tree
	r, err := replicateNamespace(cfg)
	mustOK(t, err)
	if r.Replicated != 3 || r.Removed != 0 || !r.OK() || !r.Reconciled {
		t.Fatalf("additive replication: %+v", r)
	}
	if _, _, err := dst.s.HeadObject("b", "dest/orphan"); err != nil {
		t.Fatal("additive recursion removed a destination key")
	}
	_, got, err := dst.s.GetObject("b", "dest/first")
	mustOK(t, err)
	_, m, err := dst.s.HeadObject("b", "dest/first")
	mustOK(t, err)
	if !bytes.Equal(got, body) || len(m.Metadata) != 1 || m.Metadata[0].Value != "new" {
		t.Fatal("metadata-only projection change was missed")
	}
	_, marker, err := dst.s.GetObject("b", "dest/")
	mustOK(t, err)
	if string(marker) != "directory marker" {
		t.Fatal("projection omitted the first relative key")
	}
	cfg.Mirror = true
	preview, err := planReplicationNamespace(cfg)
	mustOK(t, err)
	if preview.WouldRemove != 1 || !preview.Reconciled || preview.WouldPublish != 0 {
		t.Fatalf("mirror plan: %+v", preview)
	}
	r, err = replicateNamespace(cfg)
	mustOK(t, err)
	if r.Replicated != 0 || r.Removed != 1 || !r.OK() {
		t.Fatalf("explicit mirror: %+v", r)
	}
	if _, _, err := dst.s.HeadObject("b", "dest/orphan"); !errors.Is(err, errNoSuchKey) {
		t.Fatalf("explicit mirror left the old visible key: %v", err)
	}
	if len(historyFor(t, dst.s, "b", "dest/orphan")) != 1 {
		t.Fatal("mirror removed prior object history")
	}
	if _, _, err := dst.s.HeadObject("b", "outside/orphan"); err != nil {
		t.Fatal("mirror crossed its scope")
	}
	_, after, err := namespaceReplicationObjects(cfg)
	mustOK(t, err)
	if after.plan.rounds != 1 || len(after.plan.edits) != 0 {
		t.Fatalf("equal signed projections traversed the tree: %+v", after.plan)
	}
	if ss.projections.entries[scopeS].tree != sourceTree || ds.projections.entries[scopeD].tree != destTree {
		t.Fatal("transfer or comparison rebuilt whole projection")
	}
	put(src, "source/first", body, "next")
	_, delta, err := namespaceReplicationObjects(cfg)
	mustOK(t, err)
	if len(delta.plan.edits) != 1 || delta.plan.status[0] != 2 || ss.projections.entries[scopeS].tree != sourceTree {
		t.Fatalf("compact J2 suffix did not refresh the exact key: %+v", delta.plan)
	}
	put(dst, "dest/race", []byte("original"), "old")
	_, guarded, err := namespaceReplicationObjects(cfg)
	mustOK(t, err)
	put(dst, "dest/race", []byte("original"), "old")
	if _, err := guarded.remove(); !errors.Is(err, errProjectionStale) {
		t.Fatalf("mirror accepted changed destination: %v", err)
	}
	_, current, err := dst.s.GetObject("b", "dest/race")
	mustOK(t, err)
	if string(current) != "original" {
		t.Fatal("mirror touched a newer destination")
	}
	cfg.Source.Bucket = "missing-bucket"
	if _, err := replicateNamespace(cfg); !errors.Is(err, errNoSuchBucket) {
		t.Fatalf("mirror treated an absent source as empty: %v", err)
	}
	if _, _, err := dst.s.HeadObject("b", "dest/race"); err != nil {
		t.Fatal("missing source removed data from destination")
	}
}

func TestFB16ProjectionTransportTamperAndOldPeer(t *testing.T) {
	src, dst := newAuthLab(t), newAuthLab(t)
	src.put("initial", []byte("first payload"))
	srv, sc := fb16TestServer(t, src, "source", true, false)
	_, dc := fb16TestServer(t, dst, "destination", false, false)
	cfg := namespaceReplicateConfig{Source: sc, Dest: dc, Incremental: true}
	result, err := replicateNamespace(cfg)
	mustOK(t, err)
	if result.Reconciled || result.Replicated != 1 {
		t.Fatalf("older peer did not select the ordinary additive route: %+v", result)
	}
	tampered := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, r)
		raw := rr.Body.Bytes()
		if rr.Code == http.StatusOK && len(raw) > 0 {
			raw[len(raw)-1] ^= 1
		}
		w.WriteHeader(rr.Code)
		w.Write(raw)
	}))
	defer tampered.Close()
	cfg.Source.Endpoint = tampered.URL
	if _, _, err := namespaceReplicationObjects(cfg); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("tampered peer mislabeled as compatible fallback: %v", err)
	}
	var recorded []byte
	var replayMu sync.Mutex
	replay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		replayMu.Lock()
		defer replayMu.Unlock()
		if recorded == nil {
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, r)
			recorded = bytes.Clone(rr.Body.Bytes())
			w.WriteHeader(rr.Code)
		}
		w.Write(recorded)
	}))
	defer replay.Close()
	played := sc
	played.Endpoint = replay.URL
	at := projectionHTTP(context.Background(), played, projectionScope{bucket: "b"})
	_, err = at(projectionRootRequest, [16]byte{}, nil)
	mustOK(t, err)
	if _, err := at(projectionRootRequest, [16]byte{}, nil); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("previously authenticated root reply replayed: %v", err)
	}
	ex := projectionHTTP(context.Background(), sc, projectionScope{bucket: "b", prefix: ""})
	root, err := ex(projectionRootRequest, [16]byte{}, nil)
	mustOK(t, err)
	var token [16]byte
	copy(token[:], root)
	otherScope := projectionHTTP(context.Background(), sc, projectionScope{bucket: "b", prefix: "unrelated"})
	branch := projectionAppendIDs([]byte{0, 4}, []int{0})
	if _, err := otherScope(projectionBranchesRequest, token, branch); !errors.Is(err, errProjectionStale) {
		t.Fatalf("projection token crossed its signed scope: %v", err)
	}
	src.put("new", []byte("epoch advanced"))
	if _, err := ex(projectionBranchesRequest, token, branch); !errors.Is(err, errProjectionStale) {
		t.Fatalf("accepted stale scope token: %v", err)
	}
}

func FuzzFB16ProjectionQuery(f *testing.F) {
	row := newProjectionRow([32]byte{}, "scope-key", sha256.Sum256([]byte("body")))
	tree, err := newProjectionTree([32]byte{}, []projectionRow{row})
	if err != nil {
		f.Fatal(err)
	}
	e := &projectionCacheEntry{tree: tree}
	f.Add(byte(projectionRootRequest), []byte{})
	f.Add(byte(projectionBranchesRequest), projectionAppendIDs([]byte{0, 4}, []int{0}))
	f.Add(byte(projectionHintsRequest), projectionAppendIDs([]byte{4}, []int{projectionLeafNumber(tree.boundary, row.key)}))
	root := tree.root()
	f.Add(byte(projectionVerifyRequest), projectionAppendEdits(root[:], nil))
	f.Fuzz(func(t *testing.T, op byte, data []byte) {
		out, err := projectionQuery(e, op%6, data[:min(len(data), 8192)])
		if err == nil && len(out) > namespaceProjectionLimit {
			t.Fatal("projection exceeded its response bound")
		}
		if tree.root() != root {
			t.Fatal("query changed the tree")
		}
	})
}

func TestFB16ActualOlderPeer(t *testing.T) {
	binary := os.Getenv("ZEROS3_TEST_PRE_FB16")
	if binary == "" {
		t.Skip("set ZEROS3_TEST_PRE_FB16 to the verified pre-projection binary")
	}
	address, err := net.Listen("tcp", "127.0.0.1:0")
	mustOK(t, err)
	addr := address.Addr().String()
	mustOK(t, address.Close())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	cred := Credentials{AccessKeyID: "pre-fb16", SecretAccessKey: "test-legacy-peer"}
	proc := exec.CommandContext(ctx, binary, "serve", "-store", t.TempDir(), "-addr", addr,
		"-access-key", cred.AccessKeyID, "-secret-key", cred.SecretAccessKey)
	mustOK(t, proc.Start())
	defer func() {
		cancel()
		_ = proc.Wait()
	}()
	for {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("prior peer did not listen")
		case <-time.After(15 * time.Millisecond):
		}
	}
	old := syncClientConfig{Endpoint: "http://" + addr, Bucket: "b", Creds: cred, Region: defaultRegion}
	response, _, err := old.signAndDo(ctx, http.MethodPut, "/b", nil, nil)
	mustOK(t, err)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("prior peer could not create bucket: %d", response.StatusCode)
	}
	src := newAuthLab(t)
	src.put("only", []byte("old peer must preserve the exact bytes"))
	_, sc := fb16TestServer(t, src, "current", true, false)
	cfg := namespaceReplicateConfig{Source: sc, Dest: old, Incremental: true, NativeBatch: true}
	result, err := replicateNamespace(cfg)
	mustOK(t, err)
	if result.Reconciled || !result.OK() || result.Replicated != 1 {
		t.Fatalf("pre-projection peer fallback: %+v", result)
	}
	response, data, err := old.signAndDo(ctx, http.MethodGet, "/b/only", nil, nil)
	mustOK(t, err)
	if response.StatusCode != http.StatusOK || !bytes.Equal(data, src.want["only"]) {
		t.Fatal("prior peer got a different object")
	}
	t.Logf("older peer fallback preserved SHA-256 %x", sha256.Sum256(data))
}
