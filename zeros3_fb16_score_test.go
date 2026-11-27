package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fb16MeasuredTraffic struct {
	calls, request, response atomic.Int64
}

type fb16MeasuredWriter struct {
	http.ResponseWriter
	traffic *fb16MeasuredTraffic
}

func (m fb16MeasuredWriter) Unwrap() http.ResponseWriter { return m.ResponseWriter }
func (m fb16MeasuredWriter) Flush()                      { _ = http.NewResponseController(m.ResponseWriter).Flush() }

func (m fb16MeasuredWriter) Write(p []byte) (int, error) {
	n, err := m.ResponseWriter.Write(p)
	m.traffic.response.Add(int64(n))
	return n, err
}

type fb16MeasuredBody struct {
	io.ReadCloser
	traffic *fb16MeasuredTraffic
}

func (b fb16MeasuredBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.traffic.request.Add(int64(n))
	return n, err
}

func fb16MeasuredServer(t *testing.T, l *authLab, label string) (syncClientConfig, *fb16MeasuredTraffic) {
	t.Helper()
	c := Credentials{AccessKeyID: label + "-test", SecretAccessKey: label + "-local-fixture"}
	srv := NewServer(l.s, c, defaultRegion)
	srv.projections = &projectionCache{}
	traffic := &fb16MeasuredTraffic{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.URL.Query().Get("list-type") == "2" {
			traffic.calls.Add(1)
			r.Body = fb16MeasuredBody{r.Body, traffic}
			w = fb16MeasuredWriter{w, traffic}
		}
		srv.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return syncClientConfig{Endpoint: server.URL, Bucket: "b", Creds: c, Region: defaultRegion}, traffic
}

func TestFB16IntegratedSignedProjectionScoreboard(t *testing.T) {
	for round := range 3 {
		order := []string{"list", "projection"}
		if round%2 == 1 {
			slices.Reverse(order)
		}
		for _, lane := range order {
			t.Run(fmt.Sprintf("round%d/%s", round+1, lane), func(t *testing.T) {
				t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
				src, dest := newAuthLab(t), newAuthLab(t)
				for _, l := range []*authLab{src, dest} {
					mustOK(t, l.s.ensureFeature(journalBinFeature))
					l.s.writeBinaryJournal, l.s.writeBinaryManifests = true, true
				}
				var sample []byte
				for i := range 144 {
					key := fmt.Sprintf("obj-%04d", i)
					data := genRandomBytes(int64(7931+i), 8<<10)
					if i == 0 {
						sample = data
					}
					src.put(key, data)
					dest.put(key, data)
				}
				a, ah := fb16MeasuredServer(t, src, "from")
				b, bh := fb16MeasuredServer(t, dest, "to")
				cfg := namespaceReplicateConfig{Source: a, Dest: b, IfUnchanged: true, NativeBatch: true,
					Incremental: lane == "projection", Workers: 2}
				if cfg.Incremental {
					_, plan, err := namespaceReplicationObjects(cfg)
					mustOK(t, err)
					if !plan.active || len(plan.plan.edits) != 0 {
						t.Fatal("warming the identical projection found differences")
					}
				} else if _, err := listSourceObjects(a, ""); err != nil {
					t.Fatal(err)
				}
				for i := 1; i <= 7; i++ {
					key := fmt.Sprintf("obj-%04d", i*3)
					src.put(key, genRandomBytes(int64(16913+i), 12<<10))
				}
				for _, traffic := range []*fb16MeasuredTraffic{ah, bh} {
					traffic.calls.Store(0)
					traffic.request.Store(0)
					traffic.response.Store(0)
				}
				var wg sync.WaitGroup
				var latencies [4][24]int64
				errs := make(chan error, len(latencies))
				begin := make(chan struct{})
				for worker := range latencies {
					wg.Go(func() {
						<-begin
						for i := range latencies[worker] {
							start := time.Now()
							resp, raw, err := a.signAndDo(context.Background(), http.MethodGet, "/b/obj-0000", nil,
								map[string]string{"Range": "bytes=0-4095"})
							if err != nil || resp.StatusCode != http.StatusPartialContent || !bytes.Equal(raw, sample[:4096]) {
								errs <- fmt.Errorf("signed read mismatch: %w", err)
								return
							}
							latencies[worker][i] = time.Since(start).Nanoseconds()
						}
					})
				}
				start, cpu := time.Now(), fb13CPU()
				close(begin)
				result, err := replicateNamespace(cfg)
				elapsed, workCPU := time.Since(start), fb13CPU()-cpu
				wg.Wait()
				close(errs)
				for err := range errs {
					mustOK(t, err)
				}
				mustOK(t, err)
				if !result.OK() || result.Reconciled != cfg.Incremental || cfg.Incremental && result.Discovered != 7 {
					t.Fatalf("replication scoreboard: %+v", result)
				}
				for key, want := range src.want {
					_, got, err := dest.s.GetObject("b", key)
					mustOK(t, err)
					if !bytes.Equal(got, want) {
						t.Fatalf("integrated score lane changed %s", key)
					}
				}
				dest.verifyOK("integrated score")
				var samples []int64
				for _, row := range latencies {
					samples = append(samples, row[:]...)
				}
				disk, _, _ := fb13Allocation(t, dest.dir)
				t.Logf("lane=%s round=%d rows=%d changed=7 requests=%d request_body=%d response_body=%d upload_payload=%d destination_allocated=%d cpu=%s wall=%s signed_get_p99_ns=%d signed_samples=%d",
					lane, round+1, result.Discovered, ah.calls.Load()+bh.calls.Load(), ah.request.Load()+bh.request.Load(),
					ah.response.Load()+bh.response.Load(), result.Stats.UploadedBytes, disk, workCPU, elapsed,
					fb14Quantile(samples, 99), len(samples))
			})
		}
	}
}
