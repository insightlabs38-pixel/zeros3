package zstdz3

import (
	"bytes"
	"fmt"
	"testing"
)

func checkRanges(t *testing.T, comp []byte, opt Options) {
	t.Helper()
	opt.CheckpointEvery = 1
	var plainB, units bytes.Buffer
	res, err := Parse(bytes.NewReader(comp), &plainB, &units, opt)
	if err != nil {
		t.Fatal(err)
	}
	plain, ub := plainB.Bytes(), units.Bytes()
	if len(res.Checkpoints) == 0 {
		t.Fatal("no checkpoints")
	}
	for _, cp := range res.Checkpoints {
		b := cp.Marshal()
		back, err := UnmarshalCheckpoint(b)
		if err != nil || !bytes.Equal(back.Marshal(), b) {
			t.Fatalf("checkpoint %d/%d: canonical round trip: %v", cp.Frame, cp.Block, err)
		}
		for _, span := range []int64{1, 5000, 400000} {
			stop := min(cp.CompOff+span, int64(len(comp)))
			var out bytes.Buffer
			st, err := ReplayFrom(&out, res.Skeleton, back, bytes.NewReader(ub[cp.UnitOff:]), bytes.NewReader(plain), stop)
			if err != nil {
				t.Fatalf("checkpoint %d/%d span %d: %v", cp.Frame, cp.Block, span, err)
			}
			if !bytes.Equal(out.Bytes(), comp[cp.CompOff:cp.CompOff+int64(out.Len())]) || int64(out.Len()) < stop-cp.CompOff {
				t.Fatalf("checkpoint %d/%d span %d: ranged replay differs (%d bytes, %d blocks)", cp.Frame, cp.Block, span, out.Len(), st.Blocks)
			}
		}
	}
}

func TestRangeReplayRich(t *testing.T) {
	comp, _ := richStream(t)
	checkRanges(t, comp, Options{})
	checkRanges(t, comp, Options{NoSeqModel: true})
}

func TestRangeReplayReference(t *testing.T) {
	bin := refZstd(t, "1.5.7")
	c := corpus()
	for _, n := range []string{"mixed", "text1M", "rec"} {
		for _, lvl := range []string{"-3", "-19"} {
			comp := runZstd(t, bin, c[n], lvl, "-T1")
			t.Run(fmt.Sprintf("%s%s", n, lvl), func(t *testing.T) {
				checkRanges(t, comp, Options{})
			})
		}
	}
}
