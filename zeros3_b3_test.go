package main

import (
	"fmt"
	"testing"
)

func TestNSDebug(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	mustOK(t, err)
	defer s.Close()
	mustOK(t, s.CreateBucket("b"))
	for i := 0; i < 40; i++ {
		_, err := s.PutObject("b", fmt.Sprintf("k%03d", i), []byte("x"), "text/plain", nil)
		mustOK(t, err)
	}
	mustOK(t, s.nsConsolidate(false))
	for i := 0; i < 40; i++ {
		if e := s.tObj("b", fmt.Sprintf("k%03d", i)); e == nil {
			t.Fatalf("missing %d", i)
		}
	}
}
