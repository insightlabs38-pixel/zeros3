package ir

import (
	"fmt"
	"os"
	"testing"

	"zeros3-z3/deflate"
)

// ir/testdata/zlibfast/L{1,2,3}.deflate are raw streams of one 160 KiB text
// compressed by zlib 1.3 (python3 zlib.compressobj(level, 8, -15, 8, 0)).
func TestZlibFastEmulation(t *testing.T) {
	for lv := 1; lv <= 3; lv++ {
		raw, err := os.ReadFile(fmt.Sprintf("testdata/zlibfast/L%d.deflate", lv))
		if err != nil {
			t.Fatal(err)
		}
		s, err := deflate.Parse(raw, deflate.Options{})
		if err != nil {
			t.Fatal(err)
		}
		size := func(prof uint8, pol Policy) (int, int) {
			pk, err := Encode(s, Config{Codec: Pred, Policy: pol, Profile: prof})
			if err != nil {
				t.Fatal(err)
			}
			return pk.TotalUnitBytes() + len(pk.Skeleton), pk.Pred.Escapes
		}
		for _, pol := range []Policy{{PStream, 0}, {PTokRun, 4 << 10}} {
			exact, esc := size(uint8(3+lv), pol)
			old, _ := size(3, pol)
			wrong, _ := size(uint8(3+lv%3+1)%7+0, pol) // some other profile must still be exact, only larger
			t.Logf("level %d %v: emulated %d B (%d escapes), P3 %d B, other profile %d B, stream %d B", lv, pol, exact, esc, old, wrong, len(raw))
			if pol.Kind == PStream && exact*50 > len(raw) { // >2% of the stream
				t.Errorf("level %d: emulated corrections %d B are over 2%% of the %d B stream", lv, exact, len(raw))
			}
			if exact*3 > old {
				t.Errorf("level %d %v: emulation (%d B) is not clearly better than the non-emulating zlib-fast profile (%d B)", lv, pol, exact, old)
			}
			// exact reconstruction with the emulating profile, a wrong emulator and a wrong warm-up
			for _, prof := range []uint8{uint8(3 + lv), uint8(3 + lv%3 + 1)} {
				roundtrip(t, raw, Config{Codec: Pred, Policy: pol, Profile: prof})
			}
		}
	}
}

// A different warm-up length changes the predictions, never the bytes the
// decoder produces from units encoded with that same warm-up.
func TestZlibFastWarmupVariants(t *testing.T) {
	raw, err := os.ReadFile("testdata/zlibfast/L1.deflate")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { EmuWarm = 0 }()
	for _, warm := range []int{1, 4 << 10, 32 << 10, 100 << 10} {
		EmuWarm = warm
		roundtrip(t, raw, Config{Codec: Pred, Policy: Policy{PTokRun, 4 << 10}, Profile: 4})
	}
}
