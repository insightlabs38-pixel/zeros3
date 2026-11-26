package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFB08NativeNamespaceThousandObjects(t *testing.T) {
	for _, shared := range []bool{true, false} {
		t.Run(fmt.Sprintf("shared-payload-%v", shared), func(t *testing.T) { fb08ThousandObjects(t, shared) })
	}
}

func fb08ThousandObjects(t *testing.T, shared bool) {
	t.Helper()
	_, source, srcCreds, region := newSyncTestServer(t)
	_, destination, dstCreds, dstRegion := newSyncTestServer(t)
	src, dst := source.store, destination.store
	mustOK(t, src.CreateBucket("source"))
	mustOK(t, dst.CreateBucket("destination"))
	bodies := make([][]byte, 1000)
	keys := make([]string, 1000)
	var uploaded int64
	wantUnique := 1
	if !shared {
		wantUnique = len(keys)
	}
	for i := range keys {
		key := fmt.Sprintf("objects/%04d", i)
		if i == 11 {
			key = "objects/! space?#%"
		}
		keys[i] = key
		data := bytes.Repeat([]byte("shared payload\n"), 128)
		if !shared {
			data = bytes.Repeat([]byte(fmt.Sprintf("unique payload %04d\n", i)), 128)
		}
		bodies[i] = data
		if !shared || i == 0 {
			uploaded += int64(len(data))
		}
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
	if result.Replicated != 1000 || !result.OK() || result.Stats.UniqueChunksUploaded != wantUnique ||
		result.Stats.UploadedBytes != uploaded {
		t.Fatalf("native result: %+v", result)
	}
	mu.Lock()
	for _, want := range []string{"source POST " + nativeBatchDescribe, "destination POST " + nativeBatchDescribe,
		"destination POST " + nativeBatchNegotiate, "destination POST " + nativeBatchCommit} {
		if requests[want] != 1 {
			t.Errorf("requests[%s]=%d, want one", want, requests[want])
		}
	}
	streams := requests["destination POST "+nativeBatchStream]
	count := 0
	for _, n := range requests {
		count += n
	}
	mu.Unlock()
	if count > 12 || streams != (wantUnique+maxNativeStreamRecords-1)/maxNativeStreamRecords {
		t.Fatalf("too many requests for one native batch: %d", count)
	}
	for i, key := range keys {
		dest := "replica/" + strings.TrimPrefix(key, "objects/")
		entry, got, err := dst.GetObject("destination", dest)
		mustOK(t, err)
		man := mustManifestFor(t, dst, entry)
		if !bytes.Equal(bodies[i], got) || man.ContentType != "text/plain" ||
			len(man.Metadata) != 1 || man.Metadata[0].Value != fmt.Sprint(i) {
			t.Fatalf("different replicated object %q", dest)
		}
	}
	retry, err := replicateNamespace(cfg)
	mustOK(t, err)
	if !retry.OK() || retry.Stats.Unchanged != 1000 || retry.Stats.UploadedBytes != 0 {
		t.Fatalf("equivalent rerun was not idempotent: %+v", retry)
	}
	t.Logf("replicated 1000 objects and %d unique records in %d signed requests (%d streams); 1000 equivalent objects on rerun", wantUnique, count, streams)
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

func fb08StreamRecords(bodies [][]byte) ([]nativeStreamRecord, []syncChunkDescriptor) {
	records := make([]nativeStreamRecord, len(bodies))
	descs := make([]syncChunkDescriptor, len(bodies))
	for i, body := range bodies {
		sum := sha256.Sum256(body)
		records[i] = nativeStreamRecord{sum: sum, logical: uint32(len(body)), wire: uint32(len(body)), codec: packCodecRaw}
		descs[i] = syncChunkDescriptor{SHA256: hex.EncodeToString(sum[:]), Length: int64(len(body))}
	}
	return records, descs
}

func TestFB08NativeStreamKeepsVerifiedPrefix(t *testing.T) {
	_, srv, creds, region := newSyncTestServer(t)
	done := make(chan struct{}, 2)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.ServeHTTP(w, r)
		if r.URL.Path == nativeBatchStream {
			done <- struct{}{}
		}
	}))
	defer ts.Close()
	cfg := mustPeerConfig(ts, creds, region)
	bodies := [][]byte{b4Body(800, 8<<10), b4Body(801, 8<<10), b4Body(802, 8<<10)}
	records, descs := fb08StreamRecords(bodies)
	err := postNativeStream(t.Context(), cfg, records, func(_ context.Context, emit func(int, []byte) error) error {
		if err := emit(0, bodies[0]); err != nil {
			return err
		}
		return errors.New("fixture read ended after the first signed record")
	})
	if err == nil {
		t.Fatal("truncated signed stream was acknowledged")
	}
	reply, err := nativeBatchCall[syncChunkDescriptor, nativeBatchMissing](cfg, nativeBatchNegotiate, descs)
	mustOK(t, err)
	if reply.Count != 3 || !bytes.Equal(reply.Bits, []byte{0b110}) {
		t.Fatalf("destination advertised an incorrect resume bitmap: %+v", reply)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("truncated stream did not finish its verified-prefix flush")
	}
	emitted := 0
	err = postNativeStream(t.Context(), cfg, records[1:], func(_ context.Context, emit func(int, []byte) error) error {
		for i, body := range bodies[1:] {
			if err := emit(i, body); err != nil {
				return err
			}
			emitted++
		}
		return nil
	})
	mustOK(t, err)
	if emitted != 2 {
		t.Fatalf("resume transmitted %d records, expected only two", emitted)
	}
	for i, record := range records {
		got, err := srv.store.casRead(record.sum)
		mustOK(t, err)
		if !bytes.Equal(got, bodies[i]) {
			t.Fatal("resumed stream yielded different CAS bytes")
		}
	}
	t.Log("cut after one authenticated record, retained one durable record, retransmitted zero verified records")
}

func TestFB08NativeResumeWaitsForMatchingStream(t *testing.T) {
	records, _ := fb08StreamRecords([][]byte{b4Body(803, 1024)})
	var flights nativeStreamFlights
	finish, ok := flights.start(records)
	if !ok {
		t.Fatal("failed to register fixture stream")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := flights.await(ctx, [][32]byte{sha256.Sum256([]byte("unrelated"))}); err != nil {
		t.Fatal("unrelated digests waited for a stream")
	}
	if err := flights.await(ctx, [][32]byte{records[0].sum}); err == nil {
		t.Fatal("matching digest was treated as settled while the stream was active")
	}
	finish()
	mustOK(t, flights.await(t.Context(), [][32]byte{records[0].sum}))
}

func fb08FixtureSignedStream(t *testing.T, cfg syncClientConfig, body []byte, spans []int, modify func([]byte)) int {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, cfg.Endpoint+nativeBatchStream, nil)
	mustOK(t, err)
	request.Header.Set("Content-Encoding", "aws-chunked")
	request.Header.Set("X-Amz-Decoded-Content-Length", fmt.Sprint(len(body)))
	now := time.Now()
	mustOK(t, signSigV4Request(request, cfg.Creds, cfg.Region, sigv4SentinelStreamingHMAC, now))
	_, seed, ok := strings.Cut(request.Header.Get("Authorization"), ",Signature=")
	if !ok {
		t.Fatal("fixture signature unavailable")
	}
	signer := testSigner{accessKey: cfg.Creds.AccessKeyID, secretKey: cfg.Creds.SecretAccessKey, region: cfg.Region}
	wire := encodeAWSFrames(signedAWSFrames(signer, now, seed, body, spans))
	if modify != nil {
		modify(wire)
	}
	request.Body, request.ContentLength = io.NopCloser(bytes.NewReader(wire)), int64(len(wire))
	response, err := cfg.client().Do(request)
	mustOK(t, err)
	defer response.Body.Close()
	return response.StatusCode
}

func TestFB08NativeStreamRejectsMalformedFrames(t *testing.T) {
	for _, lane := range []string{"reorder", "duplicate", "drop", "kind", "extend", "end", "tamper"} {
		t.Run(lane, func(t *testing.T) {
			_, srv, creds, region := newSyncTestServer(t)
			ts := httptest.NewServer(srv)
			defer ts.Close()
			cfg := mustPeerConfig(ts, creds, region)
			bodies := [][]byte{b4Body(811, 8<<10), b4Body(812, 8<<10)}
			records, _ := fb08StreamRecords(bodies)
			original := records[0].sum
			if lane == "duplicate" {
				records[1] = records[0]
			}
			if lane == "kind" {
				records[0].codec = packCodecDeflate
				records[0].wire--
				bodies[0] = bodies[0][:len(bodies[0])-1]
			}
			head, end, _ := encodeNativeStreamPlan(records)
			body := append(bytes.Clone(head), bodies[0]...)
			body = append(body, bodies[1]...)
			switch lane {
			case "reorder":
				copy(body[len(head):], bodies[1])
				copy(body[len(head)+len(bodies[1]):], bodies[0])
			case "drop":
				body = body[:len(body)-len(bodies[1])]
			case "end":
				end[0] ^= 1
			}
			body = append(body, end...)
			var mutate func([]byte)
			if lane == "extend" {
				body = append(body, 0)
			}
			if lane == "tamper" {
				mutate = func(wire []byte) {
					i := bytes.Index(wire, bodies[0])
					if i < 0 {
						t.Fatal("fixture could not locate the first signed payload")
					}
					wire[i] ^= 1
				}
			}
			status := fb08FixtureSignedStream(t, cfg, body, []int{len(head), len(bodies[0])}, mutate)
			if status < 400 {
				t.Fatalf("invalid frame accepted with status %d", status)
			}
			_, err := srv.store.casRead(original)
			if lane == "end" {
				if err != nil {
					t.Fatalf("authenticated prefix was discarded after the bad end marker: %v", err)
				}
			} else if err == nil {
				t.Fatal("an invalid first record reached CAS")
			}
		})
	}
}

func FuzzFB08NativeStreamPlan(f *testing.F) {
	records, _ := fb08StreamRecords([][]byte{[]byte("first"), []byte("second")})
	head, _, decoded := encodeNativeStreamPlan(records)
	f.Add(head, decoded)
	f.Add([]byte("ZSV3"), int64(0))
	f.Fuzz(func(t *testing.T, data []byte, length int64) {
		records, end, err := readNativeStreamPlan(bytes.NewReader(data), length)
		if err != nil {
			return
		}
		encoded, canonicalEnd, decoded := encodeNativeStreamPlan(records)
		if decoded != length || !bytes.HasPrefix(data, encoded) || !bytes.Equal(end[:], canonicalEnd) {
			t.Fatal("accepted a noncanonical native stream plan")
		}
	})
}

type fb08ThrottledBody struct {
	io.ReadCloser
	start time.Time
	rate  int64
	read  int64
}

func (b *fb08ThrottledBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.read += int64(n)
	if b.rate > 0 {
		due := b.start.Add(time.Duration(b.read * 8 * int64(time.Second) / b.rate))
		if wait := time.Until(due); wait > 0 {
			time.Sleep(wait)
		}
	}
	return n, err
}

func TestFB08NativeWireSelection(t *testing.T) {
	_, src, creds, region := newSyncTestServer(t)
	st := httptest.NewServer(src)
	defer st.Close()
	source := mustPeerConfig(st, creds, region)
	caps, err := discoverZeroS3Sync(source)
	mustOK(t, err)
	lane := func(bodies [][]byte, rate int64, linkMbit int) (int64, time.Duration) {
		t.Helper()
		_, dst, creds, region := newSyncTestServer(t)
		var observed atomic.Int64
		dt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body := &fb08ThrottledBody{ReadCloser: r.Body, start: time.Now(), rate: rate}
			r.Body = body
			dst.ServeHTTP(w, r)
			observed.Add(body.read)
		}))
		defer dt.Close()
		records, descs := fb08StreamRecords(bodies)
		start := time.Now()
		wire, err := relayNativeStreams(2, source, mustPeerConfig(dt, creds, region), caps, descs, linkMbit)
		elapsed := time.Since(start)
		mustOK(t, err)
		if wire != observed.Load() {
			t.Fatalf("wire bytes: counted=%d observed=%d", wire, observed.Load())
		}
		for i, record := range records {
			data, err := dst.store.casRead(record.sum)
			mustOK(t, err)
			if !bytes.Equal(data, bodies[i]) {
				t.Fatal("wire selection changed a record")
			}
		}
		return wire, elapsed
	}
	text, entropy := make([][]byte, 8), make([][]byte, 8)
	goSource, err := os.Open("zeros3.go")
	mustOK(t, err)
	defer goSource.Close()
	for i := range text {
		text[i] = make([]byte, 64<<10)
		_, err := goSource.ReadAt(text[i], int64(i)*(320<<10))
		mustOK(t, err)
		entropy[i] = b4Body(826+i, 64<<10)
		for _, data := range [][]byte{text[i], entropy[i]} {
			_, err := src.store.casWrite(data)
			mustOK(t, err)
		}
	}
	const slow = 10_000_000
	for repeat := range 3 {
		for _, stimulus := range []struct {
			name   string
			bodies [][]byte
			rate   int64
			hint   int
		}{
			{"text-slow", text, slow, 10},
			{"entropy-slow", entropy, slow, 10},
			{"text-fast", text, 0, 1000},
		} {
			var raw, chosen int64
			var rawTime, chosenTime time.Duration
			if repeat%2 == 0 {
				raw, rawTime = lane(stimulus.bodies, stimulus.rate, 0)
				chosen, chosenTime = lane(stimulus.bodies, stimulus.rate, stimulus.hint)
			} else {
				chosen, chosenTime = lane(stimulus.bodies, stimulus.rate, stimulus.hint)
				raw, rawTime = lane(stimulus.bodies, stimulus.rate, 0)
			}
			t.Logf("wire pair=%d case=%s raw_bytes=%d selected_bytes=%d raw_ms=%.3f selected_ms=%.3f",
				repeat, stimulus.name, raw, chosen, float64(rawTime)/float64(time.Millisecond), float64(chosenTime)/float64(time.Millisecond))
			if stimulus.name == "text-slow" {
				if chosen >= raw/2 || chosenTime >= 8*rawTime/10 {
					t.Fatal("selected text compression did not improve the throttled transfer")
				}
			} else if chosen != raw {
				t.Fatal("expected the uncompressed wire format")
			}
		}
	}
}
