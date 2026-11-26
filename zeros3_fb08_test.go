package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFB08NativeNamespaceThousandObjects(t *testing.T) {
	_, source, srcCreds, region := newSyncTestServer(t)
	_, destination, dstCreds, dstRegion := newSyncTestServer(t)
	src, dst := source.store, destination.store
	mustOK(t, src.CreateBucket("source"))
	mustOK(t, dst.CreateBucket("destination"))
	data := bytes.Repeat([]byte("shared payload\n"), 128)
	keys := make([]string, 1000)
	for i := range keys {
		key := fmt.Sprintf("objects/%04d", i)
		if i == 11 {
			key = "objects/! space?#%"
		}
		keys[i] = key
		_, err := src.PutObject("source", key, data, "text/plain", map[string]string{"sequence": fmt.Sprint(i)})
		mustOK(t, err)
	}
	var mu sync.Mutex
	requests := map[string]int{}
	wrap := func(name string, srv *Server) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			requests[name+" "+r.Method+" "+r.URL.Path]++
			mu.Unlock()
			srv.ServeHTTP(w, r)
		}))
	}
	st, dt := wrap("source", source), wrap("destination", destination)
	defer st.Close()
	defer dt.Close()
	cfg := namespaceReplicateConfig{Source: mustPeerConfig(st, srcCreds, region), Dest: mustPeerConfig(dt, dstCreds, dstRegion),
		SourcePrefix: "objects", DestPrefix: "replica", NativeBatch: true, IfUnchanged: true, Workers: 4}
	cfg.Source.Bucket, cfg.Dest.Bucket = "source", "destination"
	result, err := replicateNamespace(cfg)
	mustOK(t, err)
	if result.Replicated != 1000 || !result.OK() || result.Stats.UniqueChunksUploaded != 1 ||
		result.Stats.UploadedBytes != int64(len(data)) {
		t.Fatalf("native result: %+v", result)
	}
	mu.Lock()
	for _, want := range []string{"source POST " + nativeBatchDescribe, "destination POST " + nativeBatchDescribe,
		"destination POST " + nativeBatchNegotiate, "destination POST " + nativeBatchCommit} {
		if requests[want] != 1 {
			t.Errorf("requests[%s]=%d, want one", want, requests[want])
		}
	}
	count := 0
	for _, n := range requests {
		count += n
	}
	mu.Unlock()
	if count > 10 {
		t.Fatalf("too many requests for one native batch: %d", count)
	}
	for i, key := range keys {
		dest := "replica/" + strings.TrimPrefix(key, "objects/")
		entry, got, err := dst.GetObject("destination", dest)
		mustOK(t, err)
		man := mustManifestFor(t, dst, entry)
		if !bytes.Equal(data, got) || man.ContentType != "text/plain" ||
			len(man.Metadata) != 1 || man.Metadata[0].Value != fmt.Sprint(i) {
			t.Fatalf("different replicated object %q", dest)
		}
	}
	retry, err := replicateNamespace(cfg)
	mustOK(t, err)
	if !retry.OK() || retry.Stats.Unchanged != 1000 || retry.Stats.UploadedBytes != 0 {
		t.Fatalf("equivalent rerun was not idempotent: %+v", retry)
	}
	t.Logf("replicated 1000 objects in %d signed requests with one data transfer; 1000 equivalent objects on rerun", count)
}

func TestFB08NativeControlSignedAndBounded(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	store := srv.store
	mustOK(t, store.CreateBucket("b"))
	data := []byte("contents")
	sum, err := store.casWrite(data)
	mustOK(t, err)
	digest := hex.EncodeToString(sum[:])
	ts := httptest.NewServer(srv)
	defer ts.Close()
	cfg := mustPeerConfig(ts, creds, region)
	object := syncCommitRequest{Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat,
		Hash: zeros3SyncHashAlgorithm, Bucket: "b", Key: "one", Chunks: []syncChunkDescriptor{{SHA256: digest, Length: int64(len(data))}}}
	payload, err := json.Marshal(nativeBatchEnvelope[syncCommitRequest]{Version: nativeBatchVersion, Items: []syncCommitRequest{object}})
	mustOK(t, err)
	request, err := http.NewRequest(http.MethodPost, ts.URL+nativeBatchCommit, bytes.NewReader(payload))
	mustOK(t, err)
	mustOK(t, signSigV4Request(request, creds, region, sigv4SentinelUnsignedPayload, time.Now()))
	response, err := ts.Client().Do(request)
	mustOK(t, err)
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("unsigned batch status: %d", response.StatusCode)
	}

	for _, bad := range []nativeBatchEnvelope[syncCommitRequest]{
		{Version: 2, Items: []syncCommitRequest{object}},
		{Version: nativeBatchVersion, Items: []syncCommitRequest{object, object}},
		{Version: nativeBatchVersion, Items: make([]syncCommitRequest, maxNativeBatchObjects+1)},
	} {
		body, err := json.Marshal(bad)
		mustOK(t, err)
		res, _, err := cfg.signAndDo(context.Background(), http.MethodPost, nativeBatchCommit, body, nil)
		mustOK(t, err)
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid native batch status: %d", res.StatusCode)
		}
	}
	if _, err := store.lookupObject("b", "one"); err == nil {
		t.Fatal("invalid batch published an object")
	}
	wrong := object
	wrong.Key = "wrong-digest"
	badSHA := sha256.Sum256([]byte("different"))
	wrong.ObjectSHA256 = hex.EncodeToString(badSHA[:])
	reply, err := nativeBatchItems(cfg, nativeBatchCommit, []syncCommitRequest{object, wrong})
	mustOK(t, err)
	if reply[0].Status != http.StatusOK || reply[1].Status != http.StatusConflict {
		t.Fatalf("per-item outcomes: %+v", reply)
	}
	if _, err := store.lookupObject("b", "wrong-digest"); err == nil {
		t.Fatal("wrong whole-object digest was published")
	}
}

func TestFB08NativeNegotiationFallsBackToOldPeer(t *testing.T) {
	_, source, creds, region := newSyncTestServer(t)
	_, destination, dstCreds, dstRegion := newSyncTestServer(t)
	mustOK(t, source.store.CreateBucket("source"))
	mustOK(t, destination.store.CreateBucket("destination"))
	payload := []byte("old peer compatibility")
	_, err := source.store.PutObject("source", "object", payload, "text/plain", nil)
	mustOK(t, err)
	st := httptest.NewServer(source)
	defer st.Close()
	dt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, nativeBatchPrefix) {
			t.Error("native batch reached a peer that did not advertise it")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == zeros3SyncInfoPath {
			rec := httptest.NewRecorder()
			destination.ServeHTTP(rec, r)
			var info syncDiscoveryResponse
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &info) != nil {
				t.Errorf("old peer discovery failed: %d", rec.Code)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			info.NativeBatchVersion, info.MaxBatchObjects = 0, 0
			writeSyncJSON(w, http.StatusOK, info)
			return
		}
		destination.ServeHTTP(w, r)
	}))
	defer dt.Close()
	cfg := namespaceReplicateConfig{Source: mustPeerConfig(st, creds, region), Dest: mustPeerConfig(dt, dstCreds, dstRegion),
		NativeBatch: true, Workers: 1}
	cfg.Source.Bucket, cfg.Dest.Bucket = "source", "destination"
	result, err := replicateNamespace(cfg)
	mustOK(t, err)
	if !result.OK() || result.Replicated != 1 {
		t.Fatalf("legacy peer replication: %+v", result)
	}
	_, got, err := destination.store.GetObject("destination", "object")
	mustOK(t, err)
	if !bytes.Equal(got, payload) {
		t.Fatal("compatibility fallback changed the object")
	}
}
