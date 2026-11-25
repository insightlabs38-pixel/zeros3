package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestC11BackgroundYield(t *testing.T) {
	var gate fgGate
	if !gate.waitIdle(context.Background(), nil, time.Second) {
		t.Fatal("an idle store withheld work")
	}
	gate.enter()
	defer gate.exit()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make(chan bool, 1)
	go func() { waiting <- gate.waitIdle(ctx, nil, time.Minute) }()
	for deadline := time.Now().Add(time.Second); ; {
		gate.mu.Lock()
		registered := gate.waiters > 0
		gate.mu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background work did not register a wait")
		}
		runtime.Gosched()
	}
	cancel()
	select {
	case idle := <-waiting:
		if idle {
			t.Fatal("a canceled wait reported the busy store as idle")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation failed to wake the wait")
	}

	s := &Store{backgroundYield: "adaptive"}
	s.fg.enter()
	defer s.fg.exit()
	bounded, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := s.yieldBackground(bounded); err != nil {
		t.Fatalf("sustained foreground prevented progress: %v", err)
	}
}

func TestC11GzipProofCheckpoint(t *testing.T) {
	lab := newAuthLab(t)
	want, err := os.ReadFile("testing-harnesses/testdata/b2-seek/text.l6.gz")
	mustOK(t, err)
	lab.put("data.gz", want)
	root, _, err := lab.s.repConstructAt(lab.man("data.gz"), nil, nil, 0, true)
	mustOK(t, err)

	for _, cancel := range []bool{true, false} {
		source := lab.s.newRepSource(root.Descs, nil)
		called := 0
		env := &repEnv{Src: source, Limits: repProdLimits}
		env.gzipReplay = &repIRStreamDecodeOptions{Workers: 1, Checkpoint: func() error {
			called++
			if cancel {
				return context.Canceled
			}
			return nil
		}}
		var got bytes.Buffer
		_, _, err := env.reconstruct(root.Rep, &got)
		source.close()
		if called == 0 {
			t.Fatal("gzip replay missed the checkpoint")
		}
		if cancel {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled replay: %v", err)
			}
		} else if err != nil || !bytes.Equal(got.Bytes(), want) {
			t.Fatalf("paced replay differed: %v", err)
		}
	}
}
