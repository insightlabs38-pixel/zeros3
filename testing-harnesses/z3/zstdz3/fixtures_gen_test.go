package zstdz3

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The committed fixtures are tiny (a few KB): streams a reference producer cannot be
// asked for on demand (rare coding modes, an odd count form that forces a fallback
// block, skippable frames) plus a handful of real producer outputs. testdata/MANIFEST
// records producer, version, command, SHA-256s and the feature each exercises.
// Regenerate with: Z3_WRITE_FIXTURES=1 go test ./zstdz3 -run TestWriteFixtures

type fixture struct {
	name, producer, command, feature string
	comp, plain                      []byte
	fallbacks                        int
}

func refProduce(t *testing.T, ver string, in []byte, args ...string) []byte {
	return runZstd(t, refZstd(t, ver), in, append(args, "-T1")...)
}

func fixtures(t *testing.T) []fixture {
	var out []fixture
	add := func(f fixture) { out = append(out, f) }
	text := sampleText(3000, 11)
	jsonl := corpus()["json"][:4000]
	rec := corpus()["rec"][:6000]
	add(fixture{"ref155_text.zst", "zstd 1.5.5", "zstd -3 -T1 -q -c", "single block, FSE-compressed Huffman weights, predefined/FSE tables, content checksum", refProduce(t, "1.5.5", text, "-3"), text, 0})
	add(fixture{"ref157_json.zst", "zstd 1.5.7", "zstd -19 -T1 -q -c", "optimal parsing, repeat offsets, 4-stream literals", refProduce(t, "1.5.7", jsonl, "-19"), jsonl, 0})
	add(fixture{"ref157_rec_fast.zst", "zstd 1.5.7", "zstd --fast=3 -T1 -q -c", "negative level: raw-ish literals, long offsets", refProduce(t, "1.5.7", rec, "--fast=3"), rec, 0})
	add(fixture{"ref155_zeros.zst", "zstd 1.5.5", "zstd -3 -T1 -q -c", "RLE block", refProduce(t, "1.5.5", make([]byte, 200000), "-3"), make([]byte, 200000), 0})

	f := newFx(t, true)
	f.raw(bytes.Repeat([]byte("abcd"), 8), false)
	lits := bytes.Repeat([]byte{'z'}, 40)
	f.compressed(BlockRec{LitMode: litRLE, LitForm: 0}, lits[:31], nil, false)
	f.compressed(BlockRec{LitMode: litRLE, LitForm: 1}, lits, []seqRec{{ll: 10, ml: 7, of: 4 + 9}, {ll: 30, ml: 3, of: 4}}, true)
	c, p := f.finish()
	add(fixture{"fx_rle_literals.zst", "zstdz3 test builder", "TestFixtureRLELiterals (validated by zstd 1.5.7 -d)", "RLE literals, 1- and 2-byte header forms, offset 1", c, p, 0})

	f = newFx(t, false)
	f.raw(sampleText(200, 5), false)
	var trip []seqRec
	var rl []byte
	for i := 0; i < 6; i++ {
		trip = append(trip, seqRec{ll: 5, ml: 5, of: uint32(16 + i*2)})
		rl = append(rl, []byte("qrstu")...)
	}
	f.compressed(BlockRec{LitMode: litRaw, Modes: 1<<6 | 1<<4 | 1<<2, TabBytes: []byte{5, 4, 2}}, rl, trip, true)
	c, p = f.finish()
	add(fixture{"fx_rle_tables.zst", "zstdz3 test builder", "TestFixtureRLETables (validated by zstd 1.5.7 -d)", "RLE mode for all three sequence tables", c, p, 0})

	f = newFx(t, true)
	f.raw(sampleText(300, 6), false)
	l1 := []byte("abcdefgabcdefgabcdefgabcdefgabcdefgabcdefg")
	t1 := []seqRec{{ll: 4, ml: 7, of: 4 + 30}, {ll: 0, ml: 5, of: 2}, {ll: 0, ml: 6, of: 3}, {ll: 2, ml: 5, of: 1}, {ll: 0, ml: 4, of: 3}}
	f.compressed(BlockRec{LitMode: litComp, LitForm: 2, TreeDesc: fxTree}, l1, t1, false)
	f.compressed(BlockRec{LitMode: litTreeles, LitForm: 0}, l1[:20], []seqRec{{ll: 0, ml: 5, of: 1}, {ll: 5, ml: 6, of: 2}}, true)
	c, p = f.finish()
	add(fixture{"fx_repeat_zero_ll.zst", "zstdz3 test builder", "TestFixtureRepeatOffsets style (validated by zstd 1.5.7 -d)", "repeat codes with zero literal length, direct Huffman weights, 4 streams, treeless reuse", c, p, 0})

	c, p = repeatTablesFixture(t)
	add(fixture{"fx_repeat_tables.zst", "zstdz3 test builder", "repeatTablesFixture (decoded by libzstd 1.5.7 via python-zstandard 0.25.0)", "repeat mode for all three sequence tables (LL, OF, ML) reusing the first block's tables", c, p, 0})

	a := frameOf([]byte{0x04, 0x58}, blk(true, BlockRaw, []byte("first frame "), 12))
	x := newXXH64()
	x.write([]byte("first frame "))
	a = append(a, le32(uint32(x.sum()))...)
	b := frameOf([]byte{0x00, 0x58}, blk(true, BlockRLE, []byte{'z'}, 5))
	skip := append(append(le32(magicSkip|7), le32(5)...), 9, 8, 7, 6, 5)
	add(fixture{"skip_concat.zst", "zstdz3 test builder", "concatenation of hand-framed parts", "standard + skippable + standard frames", bytes.Join([][]byte{a, skip, b}, nil), []byte("first frame zzzzz"), 0})
	return out
}

func TestWriteFixtures(t *testing.T) {
	if os.Getenv("Z3_WRITE_FIXTURES") == "" {
		t.Skip("set Z3_WRITE_FIXTURES=1 to regenerate testdata")
	}
	fx := fixtures(t)
	// the odd-count fallback stream is built in TestFallbackBlock; reuse its shape here
	var man strings.Builder
	man.WriteString("# file\tsha256\tplain-sha256\tproducer\tcommand\tfeature\n")
	sort.Slice(fx, func(i, j int) bool { return fx[i].name < fx[j].name })
	for _, f := range fx {
		if err := os.WriteFile(filepath.Join("testdata", f.name), f.comp, 0o644); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&man, "%s\t%x\t%x\t%s\t%s\t%s\n", f.name, sha256.Sum256(f.comp), sha256.Sum256(f.plain), f.producer, f.command, f.feature)
	}
	if err := os.WriteFile("testdata/MANIFEST", []byte(man.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCommittedFixtures(t *testing.T) {
	man, err := os.ReadFile("testdata/MANIFEST")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(man), "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 6 {
			t.Fatalf("bad manifest line %q", line)
		}
		comp, err := os.ReadFile(filepath.Join("testdata", f[0]))
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(comp)) != f[1] {
			t.Errorf("%s: SHA-256 differs from the manifest", f[0])
		}
		var plain bytes.Buffer
		res, err := Parse(bytes.NewReader(comp), &plain, nil, Options{})
		if err != nil {
			t.Errorf("%s: %v", f[0], err)
			continue
		}
		if fmt.Sprintf("%x", sha256.Sum256(plain.Bytes())) != f[2] {
			t.Errorf("%s: plaintext SHA-256 differs from the manifest", f[0])
		}
		roundTrip(t, comp, plain.Bytes())
		if res.Stats.Blocks[BlockFallback] != 0 {
			t.Errorf("%s: fallback blocks", f[0])
		}
		n++
	}
	if n < 6 {
		t.Fatalf("only %d fixtures", n)
	}
}

var _ = exec.Command
