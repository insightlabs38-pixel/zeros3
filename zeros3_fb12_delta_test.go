package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

type fb12DeltaInput struct {
	Name   string `json:"name"`
	Base   string `json:"base"`
	Target string `json:"target"`
}

func TestFB12StructuralAllocation(t *testing.T) {
	input := os.Getenv("ZEROS3_FB12_STRUCTURED")
	if input == "" {
		t.Skip("set ZEROS3_FB12_STRUCTURED to the C14 final corpus")
	}
	for _, family := range []string{"parquet", "sst"} {
		t.Run(family, func(t *testing.T) {
			var versions [][]byte
			for i := range 3 {
				data, err := os.ReadFile(filepath.Join(input, family, string('0'+byte(i)), "snapshot."+family))
				mustOK(t, err)
				t.Logf("version=%d bytes=%d sha256=%x", i, len(data), sha256.Sum256(data))
				versions = append(versions, data)
			}
			for lane, name := range []string{"cdc", "b9", "b10"} {
				l := newAuthLab(t)
				for i, data := range versions {
					l.put(string('0'+byte(i)), data)
				}
				start := time.Now()
				if lane != 0 {
					mustOK(t, l.s.ensureFeature(agFeature))
					_, err := l.s.AuthorityOptimize(context.Background(), OptimizePolicy{
						AuthPolicy: AuthPolicy{Order: "oldest", MaxDepth: 2, AllowDelta: true, AllowRep: true, Apply: true},
						Profile:    "archive", Canonical: true, Executables: true, Splice: lane == 2,
					})
					mustOK(t, err)
				}
				encode := time.Since(start)
				l.reclaim()
				l.exact(name)
				l.verifyOK(name)
				status, err := l.s.AuthorityStatus()
				mustOK(t, err)
				l.close()
				var allocated int64
				err = filepath.WalkDir(l.dir, func(_ string, entry fs.DirEntry, err error) error {
					if err != nil || !entry.Type().IsRegular() {
						return err
					}
					info, err := entry.Info()
					if err == nil {
						allocated += info.Sys().(*syscall.Stat_t).Blocks * 512
					}
					return err
				})
				mustOK(t, err)
				t.Logf("lane=%s allocated=%d forms=%v optimize=%s", name, allocated, status.ByForm, encode)
			}
		})
	}
}

func fb12PackAllocation(t *testing.T, chunks [][]byte) (int64, int64) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "geometry-*.pack")
	mustOK(t, err)
	w := bufio.NewWriterSize(f, 256<<10)
	_, err = w.Write(make([]byte, packHeaderSize))
	mustOK(t, err)
	compressor := newCompressor(true)
	recordHeader := make([]byte, packRecordHeaderSize)
	seen := map[[32]byte]bool{}
	for _, raw := range chunks {
		sum := sha256.Sum256(raw)
		if seen[sum] {
			continue
		}
		seen[sum] = true
		coded, _ := compressor.encode(raw)
		_, err := w.Write(recordHeader)
		mustOK(t, err)
		_, err = w.Write(coded)
		mustOK(t, err)
	}
	_, err = w.Write(make([]byte, packFooterSize+packIndexEntrySize*len(seen)))
	mustOK(t, err)
	mustOK(t, w.Flush())
	fi, err := f.Stat()
	mustOK(t, err)
	mustOK(t, f.Close())
	return fi.Size(), fi.Sys().(*syscall.Stat_t).Blocks * 512
}

func fb12BaseAllocation(t *testing.T, base []byte) int64 {
	t.Helper()
	var chunks [][]byte
	for len(base) > 0 {
		n := findCDCBoundary(base, true)
		chunks, base = append(chunks, base[:n]), base[n:]
	}
	_, n := fb12PackAllocation(t, chunks)
	return n
}

func fb12BodyAllocation(t *testing.T, base, target [32]byte, baseSize, size int64, blob []byte) (int64, int64) {
	t.Helper()
	var pieces [][]byte
	r := authRecord{SHA: target, Size: size, Gen: 1, Form: authFormDelta, Depth: 1, Bases: []xdBaseRef{{base, baseSize}}}
	start := 0
	for _, end := range authPieceEnds(blob) {
		part := blob[start:end]
		pieces = append(pieces, part)
		r.Pieces = append(r.Pieces, authPiece{SHA: sha256.Sum256(part), Len: int64(len(part))})
		start = end
	}
	_, pack := fb12PackAllocation(t, pieces)
	body, err := r.marshal()
	mustOK(t, err)
	path := filepath.Join(t.TempDir(), "authority.body")
	mustOK(t, os.WriteFile(path, body, 0600))
	fi, err := os.Stat(path)
	mustOK(t, err)
	return pack, fi.Sys().(*syscall.Stat_t).Blocks * 512
}

func fb12ValidateXD(t *testing.T, blob, base, target []byte) xdHeader {
	t.Helper()
	h, more, err := xdParseHeader(blob)
	mustOK(t, err)
	if more || h.total != int64(len(blob)) || h.sha != sha256.Sum256(target) {
		t.Fatal("candidate authority envelope has the wrong geometry")
	}
	wantBase := sha256.Sum256(base)
	if len(h.bases) != 0 && (len(h.bases) != 1 || h.bases[0].SHA != wantBase) {
		t.Fatal("candidate changed independent base identity")
	}
	sum := sha256.New()
	for i, frame := range h.frames {
		start := frame.poff
		ops, err := xdDecodeFrame(&h, i, blob[start:start+frame.plen])
		mustOK(t, err)
		for _, op := range ops {
			produced := op.lit
			if op.base >= 0 {
				produced = base[op.off : op.off+op.n]
			}
			if !bytes.Equal(produced, target[op.tpos:op.tpos+op.n]) {
				t.Fatal("native consumer did not reconstruct the target bytes")
			}
			sum.Write(produced)
		}
	}
	if !bytes.Equal(sum.Sum(nil), h.sha[:]) {
		t.Fatal("native consumer did not reconstruct the whole-object SHA-256")
	}
	return h
}

func fb12ExportXDPrograms(t *testing.T, out *bufio.Writer, h xdHeader, blob []byte) (accepted, skipped int64) {
	t.Helper()
	for _, frame := range h.frames {
		start := frame.poff
		native := blob[start : start+frame.plen]
		body := native
		if frame.mode == 1 {
			var err error
			body, err = xdInflate(native, xdMaxProgram)
			mustOK(t, err)
		}
		if len(body) > cdcMaxChunkSize {
			skipped += int64(len(body))
			continue
		}
		accepted += int64(len(body))
		var prefix [14]byte
		binary.LittleEndian.PutUint32(prefix[0:4], uint32(len(body)))
		binary.LittleEndian.PutUint32(prefix[4:8], uint32(len(native)))
		binary.LittleEndian.PutUint32(prefix[8:12], uint32(len(native)))
		prefix[12], prefix[13] = frame.mode, frame.mode
		for _, b := range [][]byte{prefix[:], body, native, native} {
			_, err := out.Write(b)
			mustOK(t, err)
		}
	}
	return
}

func TestFB12NativeDeltaMatcherMatrix(t *testing.T) {
	manifest := os.Getenv("ZEROS3_FB12_DELTA_INPUTS")
	programs := os.Getenv("ZEROS3_FB12_XD_PROGRAMS")
	if manifest == "" || programs == "" {
		t.Skip("set FB12 delta inputs and program output for the native comparison")
	}
	data, err := os.ReadFile(manifest)
	mustOK(t, err)
	var inputs []fb12DeltaInput
	mustOK(t, json.Unmarshal(data, &inputs))
	file, err := os.Create(programs)
	mustOK(t, err)
	defer file.Close()
	out := bufio.NewWriterSize(file, 256<<10)
	header := []byte("ZRC1")
	for _, n := range []uint32{cdcMaxChunkSize, packHeaderSize, packFooterSize, packRecordHeaderSize, packIndexEntrySize, defaultPackTargetBytes} {
		header = binary.LittleEndian.AppendUint32(header, n)
	}
	_, err = out.Write(header)
	mustOK(t, err)
	var accepted, skipped int64
	for _, input := range inputs {
		t.Run(input.Name, func(t *testing.T) {
			base, err := os.ReadFile(input.Base)
			mustOK(t, err)
			target, err := os.ReadFile(input.Target)
			mustOK(t, err)
			baseSHA, targetSHA := sha256.Sum256(base), sha256.Sum256(target)
			baseAllocated := fb12BaseAllocation(t, base)
			t.Logf("base_bytes=%d sha256=%x target_bytes=%d sha256=%x base_allocated=%d", len(base), baseSHA, len(target), targetSHA, baseAllocated)
			if int64(len(target)) > authWindowMin {
				fb12WindowedMatrix(t, base, target, baseSHA, targetSHA, baseAllocated)
				return
			}
			baseline := dwDcoreDefaultParams()
			budget := baseline
			budget.MaxAnchors = min(2<<20, max(1<<20, len(base)/4))
			short := budget
			short.Window, short.Slots, short.MaxProbe = 12, 8, 32
			for _, lane := range []struct {
				name   string
				params dwDcoreParams
				frame  int64
			}{{"baseline", baseline, 128 << 10}, {"size-budget", budget, 128 << 10}, {"short-anchors", short, 128 << 10}, {"larger-frames", budget, 512 << 10}} {
				at := time.Now()
				idx, err := dwDcoreBuildIndex([][]byte{base}, lane.params)
				mustOK(t, err)
				ops := idx.Match(target)
				blob, _, err := xdEncodeOpsSum([]xdSrc{{baseSHA, base}}, xdFromDw(ops), target, targetSHA, lane.frame)
				mustOK(t, err)
				encode := time.Since(at)
				h := fb12ValidateXD(t, blob, base, target)
				pack, record := fb12BodyAllocation(t, baseSHA, targetSHA, int64(len(base)), int64(len(target)), blob)
				t.Logf("lane=%s anchors=%d retained_index_bytes=%d frames=%d envelope=%d allocated_pack=%d allocated_record=%d allocated_total=%d encode=%s",
					lane.name, idx.Anchors, len(idx.table)*8, len(h.frames), len(blob), pack, record, baseAllocated+pack+record, encode)
				if lane.name == "baseline" {
					a, b := fb12ExportXDPrograms(t, out, h, blob)
					accepted, skipped = accepted+a, skipped+b
				}
			}
		})
	}
	mustOK(t, out.Flush())
	t.Logf("independent XD program bytes under record bound=%d excluded_oversize_bytes=%d", accepted, skipped)
}

func fb12WindowedMatrix(t *testing.T, base, target []byte, baseSHA, targetSHA [32]byte, baseAllocated int64) {
	t.Helper()
	migrate := authWParams(2, int64(len(base)))
	optimize := migrate
	optimize.Anchor.MaxAnchors = min(max(len(base)/8, 256<<10), rgMaxAnchors)
	shared := migrate
	shared.Anchor.MaxAnchors = min(max(len(base)/32, 128<<10), authMaxAnchors*2)
	short := shared
	short.Anchor.Window, short.Anchor.Slots, short.Anchor.MaxProbe = 12, 8, 32
	for _, lane := range []struct {
		name   string
		params dwDcoreWParams
	}{
		{"migrate", migrate}, {"optimize", optimize}, {"size-budget", shared}, {"short-anchors", short},
	} {
		b, r := bytes.NewReader(base), bytes.NewReader(target)
		at := time.Now()
		idx, err := dwDcoreBuildWIndex(b, int64(len(base)), lane.params.Anchor, lane.params.Workers, &baseSHA)
		mustOK(t, err)
		var blob bytes.Buffer
		_, err = dwDcoreWEncode(&blob, r, int64(len(target)), b, int64(len(base)), idx, lane.params)
		mustOK(t, err)
		encode := time.Since(at)
		opened, err := dwDcoreOpenWRecord(bytes.NewReader(blob.Bytes()), int64(blob.Len()))
		mustOK(t, err)
		if _, err = opened.Decode(io.Discard, b, lane.params); err != nil || opened.SHA != targetSHA || opened.BaseSHA != baseSHA {
			t.Fatalf("windowed proof failed: %v", err)
		}
		at = time.Now()
		for i := range 24 {
			off := int64(i) * (int64(len(target)) - 4096) / 23
			body, _, err := opened.DecodeRange(b, lane.params, off, 4096)
			mustOK(t, err)
			if !bytes.Equal(body, target[off:off+4096]) {
				t.Fatal("windowed range differs")
			}
		}
		rangeTime := time.Since(at)
		pack, record := fb12BodyAllocation(t, baseSHA, targetSHA, int64(len(base)), int64(len(target)), blob.Bytes())
		t.Logf("lane=%s anchors=%d retained_index_bytes=%d frames=%d envelope=%d allocated_pack=%d allocated_record=%d allocated_total=%d encode=%s exact_24_ranges=%s",
			lane.name, idx.Anchors, len(idx.table)*8, len(opened.frames), blob.Len(), pack, record, baseAllocated+pack+record, encode, rangeTime)
	}
}

func TestFB12WindowedDryAllocationMatchesPublication(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	mustOK(t, err)
	defer s.Close()
	price := &authPieceWriter{buf: make([]byte, 0, xdPieceSize)}
	actual := s.newAuthPieceWriter()
	parts := [][]byte{b4Body(101, 3000), b4Body(102, 14000), b4Body(103, 40000), b4Body(104, 120)}
	run := func(w *authPieceWriter) []authPiece {
		for _, unit := range parts {
			w.align(int64(len(unit)))
			_, err := w.Write(unit)
			mustOK(t, err)
		}
		pieces, err := w.finish()
		mustOK(t, err)
		return pieces
	}
	planned := run(price)
	for _, p := range planned {
		if _, err := s.casStat(p.SHA); err == nil {
			t.Fatal("dry pricing published a candidate piece")
		}
	}
	base := &authObj{hasRec: true}
	m := &authMigrator{s: s, refs: map[[32]byte]int{}}
	record := &authRecord{SHA: sha256.Sum256([]byte("target")), Size: 6, Gen: 1, Form: authFormDelta, Depth: 1,
		Bases: []xdBaseRef{{sha256.Sum256([]byte("base")), 4}}, Pieces: planned}
	predicted, err := m.allocatedWindowDelta(&authObj{}, base, record)
	mustOK(t, err)
	materialized := run(actual)
	if !slices.Equal(planned, materialized) {
		t.Fatalf("dry pricing changed native piece identities")
	}
	var allocated int64
	seen := map[[32]byte]bool{}
	for _, p := range materialized {
		if seen[p.SHA] {
			continue
		}
		seen[p.SHA] = true
		fi, err := os.Stat(s.chunkPath(p.SHA))
		mustOK(t, err)
		allocated += fi.Sys().(*syscall.Stat_t).Blocks * 512
	}
	raw, err := record.marshal()
	mustOK(t, err)
	path := filepath.Join(t.TempDir(), "record")
	mustOK(t, os.WriteFile(path, raw, 0600))
	fi, err := os.Stat(path)
	mustOK(t, err)
	allocated += fi.Sys().(*syscall.Stat_t).Blocks * 512
	if predicted != allocated {
		t.Fatalf("priced %d bytes, observed %d", predicted, allocated)
	}
	t.Logf("dry writer predicted exactly %d local allocated bytes across %d independent aligned pieces", allocated, len(planned))
}

func TestFB12SharedWindowConfiguration(t *testing.T) {
	const baseSize = 33 << 20
	before := authWParams(2, baseSize)
	explicit := configuredAuthWParams(2, baseSize, authWindowTuning{anchorBytes: 8, windowKiB: 512, segmentKiB: 64})
	if before.Anchor.MaxAnchors != 256<<10 || explicit.Anchor.MaxAnchors != baseSize/8 ||
		explicit.Window != 512<<10 || explicit.Segment != 64<<10 ||
		configuredAuthWParams(2, baseSize, authWindowTuning{segmentKiB: -1}).Segment != 0 {
		t.Fatalf("wrong native matcher configuration: %+v and %+v", before, explicit)
	}
}
