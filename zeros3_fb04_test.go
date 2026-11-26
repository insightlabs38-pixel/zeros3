package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
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
