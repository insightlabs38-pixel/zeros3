package zstdz3

import (
	"bytes"
	"testing"
)

// TestSynth: the synthesized frame parses, replays byte for byte, and actually
// compresses repetitive input (so it carries real sequences, not just raw blocks).
func TestSynth(t *testing.T) {
	for name, in := range map[string][]byte{
		"empty": nil, "tiny": []byte("a"), "text": sampleText(50000, 3),
		"zeros": make([]byte, 300000), "multi": bytes.Repeat(sampleText(9000, 4), 40),
	} {
		comp, err := Synth(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		res := roundTrip(t, comp, in)
		if res.Stats.Blocks[BlockFallback] != 0 {
			t.Errorf("%s: fallback blocks", name)
		}
		if len(in) > 100000 && len(comp) >= len(in)/4 {
			t.Errorf("%s: %d bytes from %d: no compression", name, len(comp), len(in))
		}
	}
}
