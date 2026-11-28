package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDFSatFlightCancellationAndCapacity(t *testing.T) {
	s := newSatReplay(true)
	workStop := make(chan struct{})
	work := func(ctx context.Context) ([]byte, error) {
		select {
		case <-workStop:
			return []byte("verified bytes"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	key := satReplayKey{bucket: "b", key: "object", manifest: "v1"}
	leader, err := s.acquire(t.Context(), key, work)
	mustOK(t, err)
	follower, err := s.acquire(t.Context(), key, work)
	mustOK(t, err)
	if leader != follower {
		t.Fatal("overlapping identical requests did not share work")
	}
	s.release(leader)
	select {
	case <-leader.done:
		t.Fatal("leader release canceled its follower")
	default:
	}
	close(workStop)
	<-follower.done
	if follower.err != nil || string(follower.data) != "verified bytes" {
		t.Fatal("follower lost its completed replay")
	}

	cancellationObserved := make(chan struct{})
	other, err := s.acquire(t.Context(), satReplayKey{bucket: "b", key: "object", manifest: "v2"}, func(ctx context.Context) ([]byte, error) {
		<-ctx.Done()
		close(cancellationObserved)
		return nil, ctx.Err()
	})
	mustOK(t, err)
	if other == follower {
		t.Fatal("an updated namespace version reused an old result")
	}
	expired, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, err := s.acquire(expired, satReplayKey{bucket: "b", key: "other"}, work); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a third lease escaped the cap: %v", err)
	}
	s.release(other)
	<-cancellationObserved
	<-other.done
	s.release(follower)
	again, err := s.acquire(t.Context(), key, work)
	mustOK(t, err)
	<-again.done
	if again == follower {
		t.Fatal("completed data was retained for later requests")
	}
	s.release(again)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != 0 || len(s.jobs) != 0 || s.stats.PlaysCanceled != 1 || s.stats.ActiveHigh != 2 {
		t.Fatalf("unexpected final queue state: %+v", s.stats)
	}
}

func TestDFSatReplayContextBeforeFetch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p := deflateTranslatePlan{index: translateIndex{BaseSize: 30, Size: 20, HeaderLen: 10, DeflateLen: 2,
		Header: make([]byte, 10), Trailer: make([]byte, 8), Frames: []translateFrame{{Start: 0, End: 16}}}}
	got, _, err := p.readContext(ctx, 0, 20, 0, func([32]byte) ([]byte, error) {
		t.Fatal("canceled replay fetched a frame")
		return nil, nil
	})
	if got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected canceled read output %v %v", got, err)
	}
}

func TestDFSatSignedScopeAndCorruption(t *testing.T) {
	raw := gf2TarFixture(t, false)
	base := gf2GzipFixture(t, raw, 1, 65<<10, "source.tar")
	target := gf2GzipFixture(t, raw, 9, 83<<10, "target.tar")
	l := newAuthLab(t)
	l.put("base", base)
	l.put("target", target)
	mustOK(t, l.s.ensureFeature(agFeature))
	gf7Publish(t, l, base, target, l.man("base"), l.man("target"))
	_, err := l.s.finishAuthorityArchive(t.Context(), 0)
	mustOK(t, err)
	srv, signer := newTestServerAndSignerFor(t, l.s)
	replay := newSatReplay(true)
	srv.researchReplay = func(ctx context.Context, bucket, key string, m *servedManifest) (objectReader, error) {
		return replay.serve(l.s, ctx, bucket, key, m)
	}
	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()
	for _, probe := range []struct {
		headers map[string]string
		want    []byte
		status  int
	}{
		{map[string]string{"Range": "bytes=31-189"}, target[31:190], http.StatusPartialContent},
		{nil, target, http.StatusOK},
	} {
		resp := doSignedRequest(t, httpSrv.Client(), httpSrv.URL, signer, "GET", "/b/target", nil, probe.headers)
		got, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		mustOK(t, err)
		if !bytes.Equal(got, probe.want) || resp.StatusCode != probe.status {
			t.Fatal("research route changed exact signed behavior")
		}
	}
	replay.mu.Lock()
	producers := replay.stats.Producers
	replay.mu.Unlock()
	if producers != 1 {
		t.Fatalf("expected only the whole request to enter the queue: got %d", producers)
	}
	resp, err := httpSrv.Client().Get(httpSrv.URL + "/b/target")
	mustOK(t, err)
	resp.Body.Close()
	if resp.StatusCode < http.StatusBadRequest {
		t.Fatal("unsigned request entered authorized replay")
	}
	l.verifyOK("verified exact native replay through research scheduling")
	httpSrv.Close()

	id := sha256.Sum256(target)
	var wire bytes.Buffer
	mustOK(t, l.s.ExportAuthorityClosure(id, &wire))
	peer := newAuthLab(t)
	mustOK(t, peer.s.ImportAuthorityClosure(&wire))
	peer.put("target", target)
	_, cfg := fb16TestServer(t, peer, "sat-repair", true, false)
	cfg.Key = "target"
	rec, err := l.s.authLoad(id)
	mustOK(t, err)
	rep, err := l.s.casRead(rec.Root.Rep)
	mustOK(t, err)
	parsed, err := unmarshalRep(rep, repProdLimits)
	mustOK(t, err)
	plan, err := parseDeflateTranslate(parsed.Recipe)
	mustOK(t, err)
	removeChunkAnyLayout(t, l.s, plan.index.Frames[0].Program)
	l.close()
	l.open()
	srv, signer = newTestServerAndSignerFor(t, l.s)
	srv.researchReplay = func(ctx context.Context, bucket, key string, m *servedManifest) (objectReader, error) {
		return replay.serve(l.s, ctx, bucket, key, m)
	}
	failed := httptest.NewServer(srv)
	defer failed.Close()
	resp = doSignedRequest(t, failed.Client(), failed.URL, signer, "GET", "/b/target", nil, nil)
	resp.Body.Close()
	if resp.StatusCode < http.StatusBadRequest {
		t.Fatal("completed replay survived a subsequent missing frame")
	}
	mustOK(t, l.s.recoverAuthorityFromPeer("b", "target", "", cfg))
	resp = doSignedRequest(t, failed.Client(), failed.URL, signer, "GET", "/b/target", nil, nil)
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	mustOK(t, err)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, target) {
		t.Fatal("native repaired representation did not reach a new scheduled response")
	}
}

func TestDFSatChannelAdmissionCancellation(t *testing.T) {
	s := newSatReplay(false)
	s.admission = make(chan struct{}, satReplaySlots)
	work := func(context.Context) ([]byte, error) { return []byte("verified"), nil }
	var held []*satReplayReader
	for range satReplaySlots {
		reader, err := s.open(t.Context(), satReplayKey{}, work)
		mustOK(t, err)
		held = append(held, reader)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, err := s.open(ctx, satReplayKey{}, work); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unreleased output leases failed to bound admission: %v", err)
	}
	for _, r := range held {
		r.close()
	}
	reader, err := s.open(t.Context(), satReplayKey{}, work)
	mustOK(t, err)
	reader.close()
	if len(s.admission) != 0 {
		t.Fatal("admission leaked a slot")
	}
}
