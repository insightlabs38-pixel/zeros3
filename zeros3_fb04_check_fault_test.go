package main

// FB-04 M-B1 independent check (X), continued.
//
// TestFB04X_FaultMatrix crashes each kind of mutation at every hook point it
// reaches (fb02Points/fb02CrashAt) and injects an ENOSPC half write and an
// EIO fsync into its journal append. After a reopen each op is wholly old or
// wholly new, a faulted op is never acknowledged or visible, and a retry
// converges. TestFB04X_LinearizableMixedHistories checks concurrent PUT,
// multipart complete, DELETE, DeleteObjects and GET with the FB-03 checker.

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
)

// fb04xFaultCase is one kind of mutation: op runs it, and state reports
// whether the store holds its new state (true) or its old one (false),
// failing the test on anything in between.
type fb04xFaultCase struct {
	name  string
	op    func(f *fb04xFixture) error
	state func(t *testing.T, f *fb04xFixture) bool
}

func fb04xFaultCases() []fb04xFaultCase {
	present := func(f *fb04xFixture, k string) bool { e, _ := f.s.objFind("b", k); return e != nil }
	hist := func(f *fb04xFixture, k string) int { h, _ := f.s.histFind("b", k); return len(h) }
	return []fb04xFaultCase{
		{"CreateBucket", func(f *fb04xFixture) error { return f.s.CreateBucket("new") }, func(t *testing.T, f *fb04xFixture) bool {
			return f.s.HeadBucket("new") == nil
		}},
		{"DeleteBucket", func(f *fb04xFixture) error { return f.s.DeleteBucket("empty") }, func(t *testing.T, f *fb04xFixture) bool {
			return f.s.HeadBucket("empty") != nil
		}},
		{"DeleteObject", func(f *fb04xFixture) error { return f.s.DeleteObject("b", "k2") }, func(t *testing.T, f *fb04xFixture) bool {
			p, h := present(f, "k2"), hist(f, "k2")
			if p == (h == 1) {
				t.Fatalf("k2 present %v with %d archived versions", p, h)
			}
			return !p
		}},
		{"DeleteObjects", func(f *fb04xFixture) error {
			return fb04xDeleteObjects(f.srv, "b", []string{"k3", "k4", "k5", "k6"})
		}, func(t *testing.T, f *fb04xFixture) bool {
			gone := 0
			for _, k := range []string{"k3", "k4", "k5", "k6"} {
				p, h := present(f, k), hist(f, k)
				if p == (h == 1) {
					t.Fatalf("%s present %v with %d archived versions", k, p, h)
				}
				if !p {
					gone++
				}
			}
			// one group is one write: a cut may leave a prefix of its frames
			// (none was acknowledged); each key is wholly old or new
			return gone == 4
		}},
		{"CreateMultipartUpload", func(f *fb04xFixture) error { _, err := f.s.CreateMultipartUpload("b", "m2", "", nil); return err }, func(t *testing.T, f *fb04xFixture) bool {
			return len(f.s.uploads) >= 2
		}},
		{"UploadPart", func(f *fb04xFixture) error {
			_, err := f.s.UploadPart("b", "mpu", f.upload, 2, []byte("part two"))
			return err
		}, func(t *testing.T, f *fb04xFixture) bool {
			_, ok := f.s.uploads[f.upload].parts[2]
			return ok
		}},
		{"AbortMultipartUpload", func(f *fb04xFixture) error { return f.s.AbortMultipartUpload("b", "mpu", f.upload) }, func(t *testing.T, f *fb04xFixture) bool {
			_, ok := f.s.uploads[f.upload]
			return !ok
		}},
		{"CompleteMultipartUpload", func(f *fb04xFixture) error {
			_, _, err := f.s.CompleteMultipartUpload("b", "mpu", f.upload, []completedPart{{PartNumber: 1, ETag: fmt.Sprintf("%x", md5Sum([]byte("part one")))}})
			return err
		}, func(t *testing.T, f *fb04xFixture) bool {
			_, open := f.s.uploads[f.upload]
			if open == present(f, "mpu") {
				t.Fatalf("upload open %v and object present %v", open, present(f, "mpu"))
			}
			return !open
		}},
		{"PruneHistory", func(f *fb04xFixture) error {
			keep := 0
			res, err := f.s.PlanHistoryPrune(historyPruneOptions{Bucket: "b", Key: "h", KeepLast: &keep})
			if err != nil {
				return err
			}
			return f.s.ApplyHistoryPrune(&res)
		}, func(t *testing.T, f *fb04xFixture) bool {
			switch h := hist(f, "h"); h {
			case 0:
				return true
			case 2:
				return false
			default:
				t.Fatalf("h has %d archived versions, want 2 or 0", h)
				return false
			}
		}},
	}
}

func TestFB04X_FaultMatrix(t *testing.T) {
	fb01NoTailWait(t)
	for _, c := range fb04xFaultCases() {
		t.Run(c.name, func(t *testing.T) {
			f := newFB04XFixture(t)
			if c.state(t, f) {
				t.Fatal("fixture already in the new state")
			}
			pts := fb02Points(func() {
				if err := c.op(f); err != nil {
					t.Fatal(err)
				}
			})
			if !c.state(t, f) {
				t.Fatal("op did not reach the new state")
			}
			landed := map[bool]int{}
			for _, point := range pts {
				if point == hookAfterAck {
					continue
				}
				f := newFB04XFixture(t)
				fb02CrashAt(t, point, func() { c.op(f) })
				f.reopen(t)
				landed[c.state(t, f)]++
				if err := c.op(f); err != nil && !(c.name == "AbortMultipartUpload" || c.name == "CompleteMultipartUpload" || (c.name == "DeleteBucket" && errors.Is(err, errNoSuchBucket))) {
					t.Fatalf("retry after crash at %s: %v", point, err)
				}
				if !c.state(t, f) {
					t.Fatalf("retry after crash at %s did not converge", point)
				}
			}
			for _, fault := range []struct {
				name string
				fn   func(string, int) (int, error)
			}{
				{"ENOSPC", func(p string, n int) (int, error) {
					if p == faultJournalWrite {
						return n / 2, syscall.ENOSPC
					}
					return n, nil
				}},
				{"EIO", func(p string, n int) (int, error) {
					if p == faultJournalSync {
						return 0, syscall.EIO
					}
					return n, nil
				}},
			} {
				f := newFB04XFixture(t)
				testJournalFault = fault.fn
				err := c.op(f)
				testJournalFault = nil
				if err == nil {
					t.Fatalf("%s: a faulted op was acknowledged", fault.name)
				}
				if c.state(t, f) {
					t.Fatalf("%s: the failed op is visible in memory", fault.name)
				}
				f.reopen(t)
				if c.state(t, f) {
					t.Fatalf("%s: the failed op landed after reopen", fault.name)
				}
				if err := c.op(f); err != nil || !c.state(t, f) {
					t.Fatalf("%s: retry: %v", fault.name, err)
				}
			}
			t.Logf("%d crash points: %d left the old state, %d the new", len(pts), landed[false], landed[true])
		})
	}
}

// ---- G-LINEAR ----

// TestFB04X_LinearizableMixedHistories records concurrent PUT, DELETE,
// DeleteObjects, multipart completion and GET on a few keys and checks
// every key's history with the FB-03 checker.
func TestFB04X_LinearizableMixedHistories(t *testing.T) {
	for seed := uint64(1); seed <= 6; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			s, _ := newBucketStore(t)
			srv := NewServer(s, Credentials{AccessKeyID: "AKIAFB04TESTACCESSKY", SecretAccessKey: "FB04TestSecretKeyForZeroS3Tests000000000"}, "us-east-1")
			rec := newLinRecorder()
			keys := []string{"a", "b", "c"}
			var wg sync.WaitGroup
			var seq atomic.Int64
			for g := 0; g < 6; g++ {
				wg.Add(1)
				go func(g int) {
					defer wg.Done()
					rng := rand.New(rand.NewPCG(seed, uint64(g)))
					for i := 0; i < 30; i++ {
						k := keys[rng.IntN(len(keys))]
						switch r := rng.IntN(10); {
						case r < 3:
							v := fmt.Sprintf("v%d", seq.Add(1))
							call := rec.now()
							_, err := s.PutObject("b", k, []byte(v), "", nil)
							ret := rec.now()
							if err != nil {
								t.Error(err)
								return
							}
							rec.add(k, linOp{call: call, ret: ret, write: true, val: v, label: "put " + v})
						case r < 4:
							v := fmt.Sprintf("m%d", seq.Add(1))
							call := rec.now()
							id, err := s.CreateMultipartUpload("b", k, "", nil)
							if err == nil {
								_, err = s.UploadPart("b", k, id, 1, []byte(v))
							}
							if err == nil {
								_, _, err = s.CompleteMultipartUpload("b", k, id, []completedPart{{PartNumber: 1, ETag: fmt.Sprintf("%x", md5Sum([]byte(v)))}})
							}
							ret := rec.now()
							if err != nil {
								t.Error(err)
								return
							}
							rec.add(k, linOp{call: call, ret: ret, write: true, val: v, label: "complete " + v})
						case r < 5:
							call := rec.now()
							err := s.DeleteObject("b", k)
							ret := rec.now()
							if err != nil {
								t.Error(err)
								return
							}
							rec.add(k, linOp{call: call, ret: ret, write: true, label: "delete"})
						case r < 6:
							call := rec.now()
							err := fb04xDeleteObjects(srv, "b", keys)
							ret := rec.now()
							if err != nil {
								t.Error(err)
								return
							}
							for _, dk := range keys {
								rec.add(dk, linOp{call: call, ret: ret, write: true, label: "delete-objects"})
							}
						default:
							call := rec.now()
							_, body, err := s.GetObject("b", k)
							ret := rec.now()
							v := string(body)
							if err != nil {
								if !errors.Is(err, errNoSuchKey) {
									t.Error(err)
									return
								}
								v = ""
							}
							rec.add(k, linOp{call: call, ret: ret, val: v, label: "get " + v})
						}
					}
				}(g)
			}
			wg.Wait()
			for _, err := range rec.check(nil) {
				t.Error(err)
			}
		})
	}
}

func fb04xDeleteObjects(srv *Server, bucket string, keys []string) error {
	w := httptest.NewRecorder()
	srv.handleDeleteObjects(w, bucket, fb04DeleteXML(keys))
	if w.Code != 200 || strings.Contains(w.Body.String(), "<Error>") {
		return fmt.Errorf("DeleteObjects: %d %s", w.Code, w.Body.String())
	}
	return nil
}
