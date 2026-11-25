package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
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

func TestC2StructuredDeltaBasesUseNativeRanges(t *testing.T) {
	for _, path := range []string{
		"testing-harnesses/z3/parquetz3/testdata/pa_snappy_v1.parquet",
		"testdata/p7/rocksdb_lz4_small.sst",
	} {
		t.Run(path, func(t *testing.T) {
			l := newAuthLab(t)
			l.s.rep.hardCodecs.Store(true)
			raw, err := os.ReadFile(path)
			mustOK(t, err)
			l.put("artifact", raw)
			root, err := l.s.repConstruct(l.man("artifact"))
			mustOK(t, err)
			ctx := l.s.newAuthCtx()
			ctx.structuredRange = true
			defer ctx.close()
			body := authRepBody{c: ctx, rec: &authRecord{
				Root: root, Size: int64(len(raw)), SHA: sha256.Sum256(raw),
			}}
			for _, off := range []int{0, len(raw) / 3, len(raw) - 4096} {
				buf := make([]byte, 3072)
				n, err := body.ReadAt(buf, int64(off))
				mustOK(t, err)
				if n != len(buf) || !bytes.Equal(buf, raw[off:off+n]) || body.data != nil {
					t.Fatalf("incorrect or materialized read at %d", off)
				}
			}
		})
	}
}

type c2ReadAt struct {
	src   *bytes.Reader
	limit int64
	reads int
}

func (r *c2ReadAt) ReadAt(p []byte, off int64) (int, error) {
	r.reads++
	if off+int64(len(p)) > r.limit {
		return 0, io.ErrUnexpectedEOF
	}
	return r.src.ReadAt(p, off)
}

func TestC2SpliceWindowReuseAndExactFailure(t *testing.T) {
	data := b5Text(987, 256<<10)
	src := &c2ReadAt{src: bytes.NewReader(data), limit: int64(len(data))}
	win := splBodyWindow{src: src, size: int64(len(data))}
	buf := make([]byte, 4096)
	for off := 0; off < 32<<10; off += len(buf) {
		n, err := win.ReadAt(buf, int64(off))
		mustOK(t, err)
		if n != len(buf) || !bytes.Equal(buf, data[off:off+n]) {
			t.Fatal("window changed verified bytes")
		}
	}
	if src.reads != 1 {
		t.Fatalf("expected one backing read, got %d", src.reads)
	}
	src.limit = 180 << 10
	uncovered := int64(160 << 10)
	if n, err := win.ReadAt(buf, uncovered); err != nil || n != len(buf) {
		t.Fatalf("speculative failure affected precise read: %d %v", n, err)
	}
	if _, err := win.ReadAt(buf, src.limit); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("required-byte failure was hidden: %v", err)
	}
}

func TestC2BoundedTargetsPreserveCurrent(t *testing.T) {
	l, _ := b6Shared(t)
	rep := l.optimize(OptimizePolicy{MaxTargets: 1})
	if rep.SkippedTargets != 2 || rep.Planned.Bytes > rep.Current.Bytes {
		t.Fatalf("budgeted plan: skipped=%d, current=%d, planned=%d",
			rep.SkippedTargets, rep.Current.Bytes, rep.Planned.Bytes)
	}
}

func TestC2NoChangeMemoInvalidates(t *testing.T) {
	l := newAuthLab(t)
	l.put("first", bytes.Repeat([]byte("literal test bytes"), 128))
	pol := b6Policy(OptimizePolicy{AuthPolicy: AuthPolicy{Apply: true}})
	run := func(policy OptimizePolicy, want bool) {
		t.Helper()
		rep, err := l.s.optimizeWithMemo(context.Background(), policy)
		mustOK(t, err)
		if rep.AnalysisReused != want || rep.Applied != 0 {
			t.Fatalf("reused=%v (want %v), applied=%d", rep.AnalysisReused, want, rep.Applied)
		}
	}
	run(pol, false)
	l.close()
	l.open()
	run(pol, true)
	pol.MaxTargets = 1
	run(pol, false)
	run(pol, true)
	l.put("second", bytes.Repeat([]byte("new bytes"), 128))
	run(pol, false)
	run(pol, true)
	l.exact("memo invalidation")
}

func TestC2ArchiveFinishProcessKill(t *testing.T) {
	for _, point := range []string{hookArchiveAfterGC, hookArchiveAfterRepack} {
		t.Run(point, func(t *testing.T) {
			b9Grouped(t, 1<<20)
			f := eqFixture(t)
			f.l.close()
			if killed, _ := runHelper(t, "archive-finish", f.l.dir, point, 1,
				"ZEROS3_RECORD_GROUPS=1"); !killed {
				t.Fatal("kill point was not reached")
			}
			f.l.open()
			f.l.exact("after kill")
			f.l.verifyOK("after kill")
			_, err := f.l.s.finishAuthorityArchive(context.Background(), 0)
			mustOK(t, err)
			f.l.close()
			f.l.open()
			f.l.exact("after rerun")
			f.l.verifyOK("after rerun")
		})
	}
}
