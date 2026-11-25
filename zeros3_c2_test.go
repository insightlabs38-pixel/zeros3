package main

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"
)

func TestC2CurrentPricingChargesLooseBlocks(t *testing.T) {
	l := newAuthLab(t)
	g, err := l.s.rgBuild(context.Background(), (OptimizePolicy{
		AuthPolicy: AuthPolicy{PriceCurrentStorage: true},
	}).norm())
	mustOK(t, err)
	defer g.close()
	data := make([]byte, 8193)
	sha := [32]byte{23}
	phys := authStagedChunkPrice(data, true)
	if phys != int64(len(data))+packRecordBytes {
		t.Fatalf("staged price %d", phys)
	}
	id := g.comp(sha, int64(len(data)), phys)
	want := (int64(len(data)) + g.block - 1) / g.block * g.block
	if got := g.cphys[id]; got != want || !g.craw[id] {
		t.Fatalf("loose price %d, want %d; raw=%v", got, want, g.craw[id])
	}
}

func TestC2GenericIncumbentWithSharedComponents(t *testing.T) {
	l, _ := b6Shared(t)
	g := b6Graph(t, l, OptimizePolicy{})
	defer g.close()
	base, _ := g.solve("global", 0)
	want := base.objective()
	// These alternatives share a component, but paying enough to activate
	// it cannot improve the generic incumbent.
	shared := g.comp([32]byte{73}, 1<<30, 1<<30)
	for _, nd := range g.nodes {
		if nd.live {
			nd.opts = append(nd.opts, &rgOpt{kind: rgRep, label: "expensive", comps: []int32{shared}})
		}
	}
	g.partition()
	got, _ := g.solve("global", 0)
	oracle, _ := g.solve("oracle", 0)
	if got.objective() > want+1 || math.Abs(got.objective()-oracle.objective()) > 1 {
		t.Fatalf("full %g, generic %g, oracle %g", got.objective(), want, oracle.objective())
	}
}

func TestC2ArchiveFinishReopensAndRepeats(t *testing.T) {
	b9Grouped(t, 1<<20)
	f := eqFixture(t)
	for i := range 2 {
		finish, err := f.l.s.finishAuthorityArchive(context.Background(), 0)
		mustOK(t, err)
		if !finish.FirstGC.Applied || !finish.FinalGC.LiveSetOK {
			t.Fatalf("pass %d did not complete", i)
		}
		f.l.close()
		f.l.open()
		f.l.exact("completed archive")
		f.l.verifyOK("completed archive")
	}
}

func TestC2ArchiveFinishFailsClosed(t *testing.T) {
	b9Grouped(t, 1<<20)
	f := eqFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.l.s.finishAuthorityArchive(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled finalization: %v", err)
	}
	p, _, _ := authRecordBytesAt(t, f.l.s, f.rec.SHA)
	before, err := os.ReadFile(p)
	mustOK(t, err)
	undo := authDamageRecord(t, f.l.s, f.rec.SHA)
	if _, err := f.l.s.finishAuthorityArchive(context.Background(), 0); !errors.Is(err, errGCUnsafe) {
		t.Fatalf("damaged finalization: %v", err)
	}
	undo()
	after, err := os.ReadFile(p)
	mustOK(t, err)
	if string(before) != string(after) {
		t.Fatal("refused pass changed the live group")
	}
	f.l.close()
	f.l.open()
	f.l.exact("after refusing damage")
}
