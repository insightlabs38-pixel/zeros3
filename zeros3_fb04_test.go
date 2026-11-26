package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestFB04DeleteGroupPublication(t *testing.T) {
	for _, format := range []string{"", "j2"} {
		t.Run(map[string]string{"": "v1", "j2": "grouped"}[format], func(t *testing.T) {
			t.Setenv("ZEROS3_JOURNAL_FORMAT", format)
			dir := t.TempDir()
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.CreateBucket("b"); err != nil {
				t.Fatal(err)
			}
			keys := make([]string, 1000)
			var setup sync.WaitGroup
			for worker := range 8 {
				setup.Go(func() {
					for i := worker; i < len(keys); i += 8 {
						keys[i] = fmt.Sprintf("object-%04d", i)
						if _, err := s.PutObject("b", keys[i], []byte(keys[i]), "text/plain", nil); err != nil {
							t.Error(err)
						}
					}
				})
			}
			setup.Wait()
			var syncs atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			type response struct {
				status int
				body   string
			}
			done := make(chan response, 1)
			testJournalFault = func(point string, _ int) (int, error) {
				if point == faultJournalSync && syncs.Add(1) == 1 {
					close(entered)
					<-release
				}
				return 0, nil
			}
			srv, signer := newTestServerAndSignerFor(t, s)
			server := httptest.NewServer(srv)
			defer server.Close()
			go func() {
				status, body := z209Do(t, server, signer, http.MethodPost, "/b?delete", deleteRequestXML(false, keys...))
				done <- response{status, body}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				close(release)
				rec := <-done
				testJournalFault = nil
				t.Fatalf("DeleteObjects did not reach sync: status=%d body=%q", rec.status, rec.body)
			}
			read := make(chan error, 1)
			go func() {
				_, body, err := s.GetObject("b", keys[len(keys)-1])
				if err == nil && !bytes.Equal(body, []byte(keys[len(keys)-1])) {
					err = errors.New("reader saw wrong data")
				}
				read <- err
			}()
			select {
			case err := <-read:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(time.Second):
				t.Error("foreground read blocked on delete sync")
			}
			close(release)
			rec := <-done
			testJournalFault = nil
			var result deleteResultXML
			if err := xml.Unmarshal([]byte(rec.body), &result); err != nil || rec.status != http.StatusOK ||
				len(result.Errors) != 0 || len(result.Deleted) != len(keys) {
				t.Fatalf("delete response: status=%d deleted=%d errors=%v (%v)", rec.status, len(result.Deleted), result.Errors, err)
			}
			if got := syncs.Load(); got != 1 {
				t.Fatalf("1,000 keys used %d journal syncs", got)
			}
			s.Close()
			reopened, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			for _, key := range keys {
				if _, _, err := reopened.GetObject("b", key); !errors.Is(err, errNoSuchKey) {
					t.Fatalf("delete was not durable for %q: %v", key, err)
				}
			}
		})
	}
}

func TestFB04DeleteGroupSyncError(t *testing.T) {
	t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
	dir := t.TempDir()
	truth := fb01Seed(t, dir, 2)
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before := fb01SHA(fb01Journal(dir))
	reqs := []*rootCommitReq{
		{bucket: "b", key: "k000", deleteRoot: true},
		{bucket: "b", key: "k000", deleteRoot: true},
		{bucket: "b", key: "k001", deleteRoot: true},
	}
	testJournalFault = func(point string, _ int) (int, error) {
		if point == faultJournalSync {
			return 0, syscall.ENOSPC
		}
		return 0, nil
	}
	defer func() { testJournalFault = nil }()
	s.submitRootCommits(reqs)
	for _, r := range reqs {
		if !errors.Is(r.err, syscall.ENOSPC) {
			t.Errorf("mutation %q was acknowledged despite failed sync: %v", r.key, r.err)
		}
	}
	if lost, _ := truth.lost(s); lost != 0 || fb01SHA(fb01Journal(dir)) != before {
		t.Fatalf("failed delete group altered %d current roots or the log", lost)
	}
}

func TestFB04SerialMutationSyncLeavesReadersUnlocked(t *testing.T) {
	for _, format := range []string{"", "j2"} {
		t.Run(map[string]string{"": "v1", "j2": "grouped"}[format], func(t *testing.T) {
			t.Setenv("ZEROS3_JOURNAL_FORMAT", format)
			s, err := OpenStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.CreateBucket("b"); err != nil {
				t.Fatal(err)
			}
			probe := func(name string, run func() error) {
				t.Helper()
				count := 0
				testJournalFault = func(point string, _ int) (int, error) {
					if point == faultJournalSync {
						count++
						if !s.mu.TryLock() {
							t.Errorf("%s held the namespace lock during sync", name)
						} else {
							s.mu.Unlock()
						}
					}
					return 0, nil
				}
				defer func() { testJournalFault = nil }()
				if err := run(); err != nil || count != 1 {
					t.Errorf("%s: %d syncs, error %v", name, count, err)
				}
			}
			probe("create bucket", func() error { return s.CreateBucket("empty") })
			probe("delete bucket", func() error { return s.DeleteBucket("empty") })
			var id, etag string
			probe("create upload", func() error {
				var err error
				id, err = s.CreateMultipartUpload("b", "upload", "text/plain", nil)
				return err
			})
			data := []byte("exact part")
			probe("upload part", func() error {
				var err error
				etag, err = s.UploadPart("b", "upload", id, 1, data)
				return err
			})
			probe("complete upload", func() error {
				_, _, err := s.CompleteMultipartUpload("b", "upload", id, []completedPart{{1, etag}})
				return err
			})
			_, got, err := s.GetObject("b", "upload")
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("multipart bytes: %q %v", got, err)
			}
			id, err = s.CreateMultipartUpload("b", "aborted", "text/plain", nil)
			if err != nil {
				t.Fatal(err)
			}
			probe("abort upload", func() error { return s.AbortMultipartUpload("b", "aborted", id) })
			if err := s.DeleteObject("b", "upload"); err != nil {
				t.Fatal(err)
			}
			plan, err := s.PlanHistoryPrune(historyPruneOptions{Bucket: "b", KeepLast: pruneKeep(0)})
			if err != nil {
				t.Fatal(err)
			}
			probe("prune history", func() error { return s.ApplyHistoryPrune(&plan) })
		})
	}
}

type fb04Operation struct {
	kind, input, output string
	start, end          uint64
	conflict            bool
}

func fb04HasOrder(ops []fb04Operation, left uint32, value, final string) bool {
	if left == 0 {
		return value == final
	}
	for i, op := range ops {
		bit := uint32(1) << i
		if left&bit == 0 {
			continue
		}
		ordered := true
		for n, earlier := range ops {
			if left&(uint32(1)<<n) != 0 && earlier.end < op.start {
				ordered = false
				break
			}
		}
		if !ordered {
			continue
		}
		next := value
		switch op.kind {
		case "read":
			if op.output != value {
				continue
			}
		case "delete":
			next = ""
		case "create":
			if op.conflict != (value != "") {
				continue
			}
			if !op.conflict {
				next = op.input
			}
		case "put":
			next = op.input
		}
		if fb04HasOrder(ops, left&^bit, next, final) {
			return true
		}
	}
	return false
}

func TestFB04ConcurrentRootLinearizability(t *testing.T) {
	for _, format := range []string{"", "j2"} {
		t.Run(map[string]string{"": "v1", "j2": "grouped"}[format], func(t *testing.T) {
			t.Setenv("ZEROS3_JOURNAL_FORMAT", format)
			dir := t.TempDir()
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.CreateBucket("b"); err != nil {
				t.Fatal(err)
			}
			read := func(key string) (string, error) {
				_, b, err := s.GetObject("b", key)
				if errors.Is(err, errNoSuchKey) {
					return "", nil
				}
				return string(b), err
			}
			regimes := []string{"read", "delete", "create", "put"}
			random := rand.New(rand.NewPCG(45, 273))
			final := map[string]string{}
			for round := range 32 {
				key := fmt.Sprintf("key-%d", round)
				ops := make([]fb04Operation, 8)
				var wg sync.WaitGroup
				var event atomic.Uint64
				start := make(chan struct{})
				for i := range ops {
					op := &ops[i]
					op.kind, op.input = regimes[random.IntN(len(regimes))], fmt.Sprintf("value-%d", i)
					wg.Go(func() {
						<-start
						op.start = event.Add(1)
						var err error
						switch op.kind {
						case "read":
							op.output, err = read(key)
						case "delete":
							err = s.DeleteObject("b", key)
						case "put":
							_, err = s.PutObject("b", key, []byte(op.input), "text/plain", nil)
						case "create":
							_, err = s.PutObjectChecked("b", key, []byte(op.input), "text/plain", nil, putCondition{ifNoneMatchStar: true})
							if errors.Is(err, errPreconditionFailed) {
								op.conflict, err = true, nil
							}
						}
						op.end = event.Add(1)
						if err != nil {
							t.Errorf("%s: %v", op.kind, err)
						}
					})
				}
				close(start)
				wg.Wait()
				value, err := read(key)
				if err != nil || !fb04HasOrder(ops, (1<<len(ops))-1, "", value) {
					t.Fatalf("no legal order for %s: final=%q ops=%+v err=%v", key, value, ops, err)
				}
				final[key] = value
			}
			s.Close()
			s, err = OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			for key, want := range final {
				if got, err := read(key); err != nil || got != want {
					t.Fatalf("replay %s: got %q, want %q (%v)", key, got, want, err)
				}
			}
		})
	}
}
