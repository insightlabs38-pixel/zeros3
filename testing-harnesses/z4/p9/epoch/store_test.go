package epoch

import (
	"bytes"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestLeaseAndConcurrentCommitReconciliation(t *testing.T) {
	s := newStore(t.TempDir())
	must(t, s.put("k", []byte("old lease")))
	must(t, s.put("deleted", []byte("live at mark time")))
	reader, err := s.acquire("k")
	must(t, err)
	p, err := s.begin()
	must(t, err)
	rebuilt, err := p.stage()
	must(t, err)
	must(t, s.put("k", []byte("new write")))
	must(t, s.put("other", []byte("concurrent write absent from initial mark")))
	s.delete("deleted")
	must(t, p.publish(rebuilt))
	if _, err := s.acquire("deleted"); !os.IsNotExist(err) {
		t.Fatalf("reconciliation resurrected deleted key: %v", err)
	}
	must(t, s.reclaim())
	old, err := reader.read()
	must(t, err)
	if !bytes.Equal(old, []byte("old lease")) {
		t.Fatal("an old locator did not stay exact")
	}
	must(t, reader.release())
	if _, err := os.Stat(reader.loc.pack.path); !os.IsNotExist(err) {
		t.Fatalf("released pack was not retired: %v", err)
	}
	for key, want := range map[string]string{"k": "new write", "other": "concurrent write absent from initial mark"} {
		l, err := s.acquire(key)
		must(t, err)
		got, err := l.read()
		must(t, err)
		must(t, l.release())
		if string(got) != want {
			t.Fatalf("reconciliation dropped %s", key)
		}
	}
}

func TestMixedVerifiedReadsDuringPackPublication(t *testing.T) {
	s := newStore(t.TempDir())
	const keys, writes, repacks = 24, 192, 32
	names := make([]string, keys)
	for i := range names {
		names[i] = fmt.Sprintf("key-%d", i)
		must(t, s.put(names[i], []byte(fmt.Sprintf("%s/%d", names[i], 0))))
	}
	want := func(k string, i int) []byte { return []byte(fmt.Sprintf("%s/%d", k, i)) }
	var wg sync.WaitGroup
	var reads, attempts int
	failure := make(chan error, 4)
	start := time.Now()
	wg.Go(func() {
		for i := 1; i <= writes; i++ {
			k := names[i%keys]
			if err := s.put(k, want(k, i)); err != nil {
				failure <- err
				return
			}
		}
	})
	wg.Go(func() {
		for range repacks {
			p, err := s.begin()
			if err != nil {
				failure <- err
				return
			}
			image, err := p.stage()
			if err != nil {
				p.abort()
				failure <- err
				return
			}
			if err := p.publish(image); err != nil {
				failure <- err
				return
			}
			if err := s.reclaim(); err != nil {
				failure <- err
				return
			}
			attempts++
		}
	})
	wg.Go(func() {
		for sequence := range 768 {
			k := names[sequence%keys]
			l, err := s.acquire(k)
			if err != nil {
				failure <- err
				return
			}
			data, err := l.read()
			if releaseErr := l.release(); releaseErr != nil {
				failure <- releaseErr
				return
			}
			if err != nil {
				failure <- err
				return
			}
			if !bytes.Equal(data, want(k, 0)) {
				for step := sequence % keys; step <= writes; step += keys {
					if bytes.Equal(data, want(k, step)) {
						goto verified
					}
				}
				failure <- fmt.Errorf("reader saw an unknown version of %s", k)
				return
			}
		verified:
			reads++
		}
	})
	wg.Wait()
	close(failure)
	for err := range failure {
		must(t, err)
	}
	for i, k := range names {
		latest := writes - (writes-i)%keys
		l, err := s.acquire(k)
		must(t, err)
		data, err := l.read()
		must(t, err)
		must(t, l.release())
		if !bytes.Equal(data, want(k, latest)) {
			t.Fatalf("final acknowledged version changed for %s", k)
		}
	}
	t.Logf("%d writes, %d independently checked concurrent reads and %d publications in %s",
		writes, reads, attempts, time.Since(start))
}
