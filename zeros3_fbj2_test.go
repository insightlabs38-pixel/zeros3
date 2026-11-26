package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

// A surviving acknowledgment slot is a lower bound. Exercise the namespace
// opener and inspect against the same independently recorded acknowledged key.
func TestFBJ2IncompleteWatermarkEvidence(t *testing.T) {
	for _, mode := range []string{"lazy", "sync"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("ZEROS3_JOURNAL_WATERMARK", mode)
			dir := t.TempDir()
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.CreateBucket("b"); err != nil {
				t.Fatal(err)
			}
			truth := newFB01Truth()
			truth.put(t, s, "acknowledged", "whole acknowledged value")
			newest := fb01rReadAck(t, dir)
			if newest.unpaired || newest.FrameOff <= 0 {
				t.Fatalf("fixture did not persist two generations: %+v", newest)
			}

			healthy := fb01Copy(t, dir)
			originalBoot := testBootID
			defer func() { testBootID = originalBoot }()
			if mode == "sync" {
				fb01rOtherBoot(t)
			}
			for _, shape := range []string{"partial", "crc"} {
				t.Run(shape, func(t *testing.T) {
					damaged := fb01Copy(t, healthy)
					ack := fb01rRead(t, fb01rAckPath(damaged))
					ack[int(newest.Gen%2)*journalWatermarkPage+journalWatermarkSlot-1] ^= 1
					if err := os.WriteFile(fb01rAckPath(damaged), ack, 0o644); err != nil {
						t.Fatal(err)
					}
					survivor := fb01rReadAck(t, damaged)
					if !survivor.unpaired || survivor.Offset != newest.FrameOff {
						t.Fatalf("want the older lower bound, got %+v", survivor)
					}
					image := fb01rRead(t, fb01Journal(damaged))
					if shape == "partial" {
						image = image[:len(image)-1]
					} else {
						image[len(image)-1] ^= 1
					}
					if err := os.WriteFile(fb01Journal(damaged), image, 0o644); err != nil {
						t.Fatal(err)
					}
					check, err := inspectJournal(damaged)
					if err != nil || check.Recoverable || !strings.Contains(check.TailProof, "no coherent companion slot") {
						t.Errorf("inspect did not refuse uncertain recovery: %+v, %v", check, err)
					}
					open, err := OpenStore(damaged)
					if open != nil {
						open.Close()
					}
					if !errors.Is(err, errJournalRefused) {
						t.Errorf("owner accepted uncertain recovery: %v", err)
					}
					if !bytes.Equal(fb01rRead(t, fb01Journal(damaged)), image) ||
						!bytes.Equal(fb01rRead(t, fb01rAckPath(damaged)), ack) {
						t.Fatal("refusal changed the evidence")
					}
				})
			}
			control, err := OpenStore(healthy)
			if err != nil {
				t.Fatal(err)
			}
			defer control.Close()
			if lost, _ := truth.lost(control); lost != 0 {
				t.Fatalf("clean control lost %d acknowledged mutations", lost)
			}
		})
	}
}
