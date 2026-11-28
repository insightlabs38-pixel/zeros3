package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type gf7SourceBlock = translateSourceBlock
type gf7Frame = translateFrame
type gf7Index = translateIndex

type gf7Position struct{ block, token int }
type gf7Catalog struct {
	positions [][]int
	index     map[uint64][2]gf7Position
}

func gf7Lengths(b repDFBlock) ([]uint8, []uint8) {
	if b.Kind == repDFFixed {
		return repDFfixedLitL, repDFfixedDstL
	}
	return b.LitLens, b.DistLens
}

func gf7TokenBits(t repDFToken, l, d []uint8) int {
	sym := t.Sym()
	n := int(l[sym])
	if sym >= 257 {
		n += int(repDFlenExtra[sym-257] + d[t.DistCode()] + repDFdistExtr[t.DistCode()])
	}
	return n
}

func gf7Key(tokens []repDFToken, mask uint32) uint64 {
	x := uint64(uint32(tokens[0])&mask) | uint64(uint32(tokens[1])&mask)<<32
	y := uint64(uint32(tokens[2])&mask) | uint64(uint32(tokens[3])&mask)<<32
	return dwDcore_mix64(x) ^ dwDcore_mix64(y+0x9e3779b97f4a7c15)
}

func gf7CatalogOf(s *repDFStream, mask uint32) gf7Catalog {
	result := gf7Catalog{positions: make([][]int, len(s.Blocks)), index: map[uint64][2]gf7Position{}}
	for k, b := range s.Blocks {
		if len(b.Tokens) == 0 {
			continue
		}
		l, d := gf7Lengths(b)
		pos := int(b.StartBit) + 3 + b.HeaderBits
		positions := make([]int, len(b.Tokens)+1)
		for i, token := range b.Tokens {
			positions[i] = pos
			pos += gf7TokenBits(token, l, d)
			if i%4 != 0 || len(b.Tokens)-i < 4 {
				continue
			}
			key := gf7Key(b.Tokens[i:], mask)
			row := result.index[key]
			hit := gf7Position{k + 1, i}
			if row[0].block == 0 {
				row[0] = hit
			}
			row[1] = hit
			result.index[key] = row
		}
		positions[len(b.Tokens)] = pos
		result.positions[k] = positions
	}
	return result
}

// Extract the original block into the same byte geometry as an
// independently regenerated block. Neighbouring contributions are zero.
func gf7RawBlock(raw []byte, start, end int) []byte {
	out := slices.Clone(raw[start/8 : (end+7)/8])
	out[0] &= byte(255 << (start % 8))
	if end%8 != 0 {
		out[len(out)-1] &= byte(1<<(end%8) - 1)
	}
	return out
}

type gf7Stats struct {
	Tokens, Matched, CoveredBits, Corrections int64
	PlannedBlocks                             int
}

func gf7CompileBlock(b repDFBlock, c gf7Catalog, source *repDFStream, mask uint32) ([]byte, gf7Stats) {
	l, d := gf7Lengths(b)
	var result []byte
	matches := gf7Stats{Tokens: int64(len(b.Tokens))}
	literal := 0
	emit := func(end int) {
		if end <= literal {
			return
		}
		result = binary.AppendUvarint(result, uint64(end-literal)<<1)
		for _, v := range b.Tokens[literal:end] {
			result = binary.AppendUvarint(result, uint64(v))
		}
	}
	for i := 0; i+8 <= len(b.Tokens); {
		best, at := 0, gf7Position{}
		for _, p := range c.index[gf7Key(b.Tokens[i:], mask)] {
			if p.block == 0 {
				continue
			}
			src := source.Blocks[p.block-1].Tokens[p.token:]
			n := 0
			for n < min(len(src), len(b.Tokens)-i) && (uint32(src[n])^uint32(b.Tokens[i+n]))&mask == 0 {
				n++
			}
			if n > best {
				best, at = n, p
			}
		}
		if best < 8 {
			i++
			continue
		}
		emit(i)
		result = binary.AppendUvarint(result, uint64(best)<<1|1)
		result = binary.AppendUvarint(result, uint64(at.block-1))
		sourcePos := c.positions[at.block-1]
		begin, end := sourcePos[at.token], sourcePos[at.token+best]
		result = binary.AppendUvarint(result, uint64(begin))
		result = binary.AppendUvarint(result, uint64(end-begin))
		matches.Matched += int64(best)
		for j := range best {
			matches.CoveredBits += int64(gf7TokenBits(b.Tokens[i+j], l, d))
			if mask != ^uint32(0) {
				diff := uint32(b.Tokens[i+j]^source.Blocks[at.block-1].Tokens[at.token+j]) >> 14
				if diff != 0 {
					matches.Corrections++
				}
				result = binary.AppendUvarint(result, uint64(diff))
			}
		}
		i += best
		literal = i
	}
	emit(len(b.Tokens))
	return result, matches
}

func gf7Compile(t *testing.T, base, target []byte, mode string, parts []repMember, emit func([]byte) [32]byte) ([]byte, []repMember, gf7Stats) {
	t.Helper()
	baseHeader, source, err := gf2SplitGzip(base)
	mustOK(t, err)
	targetHeader, stream, err := gf2SplitGzip(target)
	mustOK(t, err)
	baseBits := base[baseHeader : len(base)-8]
	targetBits := target[targetHeader : len(target)-8]

	idx := gf7Index{BaseSize: len(base), Size: len(target), HeaderLen: targetHeader, DeflateLen: len(targetBits),
		Header: target[:targetHeader], Trailer: target[len(target)-8:], Tail: stream.TailPadBits, Pad: stream.TailPadLen, Root: sha256.Sum256(target)}
	members := slices.Clone(parts)
	for _, m := range parts {
		idx.Sources = append(idx.Sources, m.SHA)
		idx.SourceSizes = append(idx.SourceSizes, int(m.Size))
	}
	mask := ^uint32(0)
	if mode == "distance-corrected" {
		mask = (1 << 14) - 1
	}
	var c gf7Catalog
	if mode != "direct" {
		c = gf7CatalogOf(source, mask)
		for _, block := range source.Blocks {
			lit, dist := gf7Lengths(block)
			idx.SourceTables = append(idx.SourceTables, gf7SourceBlock{lit, dist})
		}
	}
	var stats gf7Stats
	for _, block := range stream.Blocks {
		first, last := int(block.StartBit), int(block.EndBit)
		raw := gf7RawBlock(targetBits, first, last)
		f := gf7Frame{Start: first, End: last, Raw: true, Sum: sha256.Sum256(raw)}
		encoded := raw
		if mode != "direct" && block.Kind != repDFStored && len(block.Tokens) > 0 {
			r, work := gf7CompileBlock(block, c, source, mask)
			stats.Tokens += work.Tokens
			stats.Matched += work.Matched
			stats.CoveredBits += work.CoveredBits
			stats.Corrections += work.Corrections
			meta := repIRBlockMeta{Final: block.Final, Kind: block.Kind, NTokens: len(block.Tokens),
				HLIT: block.HLIT, HDIST: block.HDIST, HCLEN: block.HCLEN, CLLens: block.CLLens, CLSyms: block.CLSyms}
			metaBytes := len(repIRencodeBlockMeta(nil, &meta, 0))
			if packPriceLen(r)+int64(metaBytes)+32 < packPriceLen(raw) {
				f.Raw, f.Header = false, meta
				encoded = r
				load := func(off, n int) ([]byte, error) { return baseBits[off : off+n], nil }
				proved, err := translateReplayFrame(encoded, f, idx.SourceTables, load, mask != ^uint32(0))
				mustOK(t, err)
				if !bytes.Equal(raw, proved) {
					t.Fatal("translated frame changed its compressed bits")
				}
				stats.PlannedBlocks++
			}
		}
		f.Program = emit(encoded)
		members = append(members, repMember{Role: repStored, SHA: f.Program, Size: int64(len(encoded))})
		idx.Frames = append(idx.Frames, f)
	}
	// Store the base header size alongside the checksummed catalog. The
	// fixed-width prefix carries the source offset without retaining tokens.
	index, err := json.Marshal(idx)
	mustOK(t, err)
	wire := binary.LittleEndian.AppendUint32(nil, uint32(baseHeader))
	if mode == "distance-corrected" {
		wire = append(wire, 1)
	} else {
		wire = append(wire, 0)
	}
	wire = append(wire, xdDeflate(index, packPriceLevel)...)
	if len(wire) > repDirectMax {
		t.Fatal("translation catalog overflow")
	}

	return wire, members, stats
}

func gf7Build(t *testing.T, l *authLab, base, target []byte, mode string) ([32]byte, gf7Stats) {
	t.Helper()
	b, err := l.s.newPackBuilder(tierHot, newCompressor(true), 500)
	mustOK(t, err)
	defer b.Abort()
	seen := map[[32]byte]bool{}
	emit := func(data []byte) [32]byte { return gf2AddPackRecord(b, seen, data) }
	var parts []repMember
	for at := 0; at < len(base); at += gf2FrameSize {
		end := min(at+gf2FrameSize, len(base))
		parts = append(parts, repMember{Role: repStored, SHA: emit(base[at:end]), Size: int64(end - at)})
	}
	wire, _, stats := gf7Compile(t, base, target, mode, parts, emit)
	root := emit(wire)
	path, entries, err := b.Finish()
	mustOK(t, err)
	_, err = l.s.publishPack(tierHot, path, entries)
	mustOK(t, err)
	return root, stats
}

func gf7Read(s *Store, id [32]byte, start, length int) ([]byte, int64, error) {
	raw, err := s.casRead(id)
	if err != nil {
		return nil, 0, err
	}
	plan, err := parseDeflateTranslate(raw)
	if err != nil {
		return nil, 0, err
	}
	return plan.read(start, length, len(raw), s.casRead)
}

func gf7Panel(t *testing.T, name string, base, target []byte) {
	t.Helper()
	for _, mode := range []string{"direct", "token-copy", "distance-corrected"} {
		t.Run(mode, func(t *testing.T) {
			l := newAuthLab(t)
			before, cpu := time.Now(), fb13CPU()
			id, stats := gf7Build(t, l, base, target, mode)
			buildWall, buildCPU := time.Since(before).Nanoseconds(), int64(fb13CPU()-cpu)
			l.close()
			alloc, pack, _ := fb13Allocation(t, l.dir)
			l.open()
			var whole, rangeNS, work []int64
			for range 3 {
				before = time.Now()
				got, _, err := gf7Read(l.s, id, 0, len(target))
				whole = append(whole, time.Since(before).Nanoseconds())
				mustOK(t, err)
				if !bytes.Equal(got, target) {
					t.Fatal("token translation did not restore exact compressed bytes")
				}
			}
			for i := range 26 {
				n := min(4096, len(target))
				at := (len(target) - n) * i / 25
				before = time.Now()
				got, cost, err := gf7Read(l.s, id, at, n)
				rangeNS = append(rangeNS, time.Since(before).Nanoseconds())
				work = append(work, cost)
				mustOK(t, err)
				if !bytes.Equal(got, target[at:at+n]) {
					t.Fatal("translated range differs")
				}
			}
			row, err := json.Marshal(map[string]any{"family": name, "lane": mode, "allocated": alloc, "pack": pack,
				"build_wall_ns": buildWall, "build_cpu_ns": buildCPU, "whole_ns": gf0Percentiles(whole), "range_ns": gf0Percentiles(rangeNS),
				"range_decoded_record_p95": fb14Quantile(work, 95), "stats": stats, "logical": len(base) + len(target)})
			mustOK(t, err)
			fmt.Printf("GF7 %s\n", row)
		})
	}
}

func TestGF7TranslatedControl(t *testing.T) {
	raw := gf2TarFixture(t, false)
	one := gf2GzipFixture(t, raw, 1, 65<<10, "source.tar")
	two := gf2GzipFixture(t, raw, 9, 83<<10, "different.tar")
	gf7Panel(t, "compressor-settings", one, two)
	if _, err := translateReplayFrame([]byte{255, 255}, gf7Frame{Start: 0, End: 10, Header: repIRBlockMeta{Kind: repDFFixed, NTokens: 5}}, nil,
		func(int, int) ([]byte, error) { return nil, io.ErrClosedPipe }, false); err == nil {
		t.Fatal("malformed translated frame accepted")
	}
}

func TestGF7NativeTranslationPanel(t *testing.T) {
	root, families := gf0Corpus(t)
	for _, fam := range families {
		if fam.Name != "typescript-gzip" && fam.Name != "typescript-gzip-adjacent" && fam.Name != "urllib3-gzip" {
			continue
		}
		t.Run(fam.Name, func(t *testing.T) {
			b, _ := gf0ReadFixture(t, root, fam, 0)
			q, _ := gf0ReadFixture(t, root, fam, 1)
			gf7Panel(t, fam.Name, b, q)
		})
	}
}

func gf7Publish(t *testing.T, l *authLab, base, target []byte, baseManifest, targetManifest manifestV1) gf7Stats {
	t.Helper()
	b := l.s.newCASBatch()
	defer b.Abort()
	emit := func(data []byte) [32]byte {
		id, err := b.Add(data)
		mustOK(t, err)
		return id
	}
	var parts []repMember
	for _, part := range gf1NativeParts(t, base, baseManifest) {
		parts = append(parts, part.member)
	}
	wire, members, stats := gf7Compile(t, base, target, "distance-corrected", parts, emit)
	rep := repObject{Kind: repKindDeflateTranslate, Recipe: wire, Members: members, Size: int64(len(target))}
	content, err := rep.marshal()
	mustOK(t, err)
	repID := emit(content)
	mustOK(t, b.Flush())
	sum := sha256.Sum256(target)
	rec := &authRecord{SHA: sum, Size: int64(len(target)), Gen: 1, Form: authFormRep,
		Root: &repRoot{Rep: repID, ObjSHA: sum, ObjSize: int64(len(target)), ManifestUUID: targetManifest.ManifestUUID}}
	_, _, err = l.s.authProve(rec, 8)
	mustOK(t, err)
	mustOK(t, l.s.authPublish(rec))
	return stats
}

func TestGF7TranslatedAuthority(t *testing.T) {
	raw := gf2TarFixture(t, false)
	one := gf2GzipFixture(t, raw, 1, 65<<10, "source.tar")
	two := gf2GzipFixture(t, raw, 9, 83<<10, "different.tar")
	l := newAuthLab(t)
	l.put("base", one)
	l.put("target", two)
	mustOK(t, l.s.ensureFeature(agFeature))
	stats := gf7Publish(t, l, one, two, l.man("base"), l.man("target"))
	if stats.PlannedBlocks == 0 {
		t.Fatal("authority fixture did not translate a block")
	}
	mustOK(t, l.s.DeleteObject("b", "base"))
	delete(l.want, "base")
	_, err := l.s.finishAuthorityArchive(t.Context(), 0)
	mustOK(t, err)
	l.close()
	l.open()
	if !slices.Contains(l.s.formatSnapshot().Features, deflateTranslateFeature) {
		t.Fatal("missing translation capability")
	}
	if old := os.Getenv("ZEROS3_GF2_OLD_BINARY"); old != "" {
		l.close()
		out, stderr, status := runZeros3CLI(t, old, "verify", "-store", l.dir, "-deep")
		if status == 0 || !strings.Contains(out+stderr, deflateTranslateFeature) {
			t.Fatalf("older reader did not refuse translated authority: %d %s %s", status, out, stderr)
		}
		l.open()
	}
	l.exact("translated source survived pruning")
	l.verifyOK("translated exact authority")
	var wire bytes.Buffer
	sum := sha256.Sum256(two)
	caps := l.s.AuthorityCaps()
	caps.Features = slices.DeleteFunc(caps.Features, func(feature string) bool { return feature == deflateTranslateFeature })
	if err := l.s.ExportAuthorityClosureFor(sum, caps, nil, &wire); !errors.Is(err, errAuthTransferUnsupported) || wire.Len() != 0 {
		t.Fatalf("negotiated translation without its feature: %v, %d bytes", err, wire.Len())
	}
	mustOK(t, l.s.ExportAuthorityClosureFor(sum, l.s.AuthorityCaps(), nil, &wire))
	dst := newAuthLab(t)
	mustOK(t, dst.s.ImportAuthorityClosure(&wire))
	dst.put("target", two)
	dst.exact("imported translated authority")
	dst.verifyOK("imported translated authority")

	entries, err := l.s.captureSnapshotEntries("b", "")
	mustOK(t, err)
	snapshot := snapshotDescriptorV1{
		SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(),
		CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: entries,
	}
	mustOK(t, l.s.publishSnapshot(snapshot))
	var bundle bytes.Buffer
	_, err = l.s.exportBundle(snapshot.SnapshotID, &bundle, true)
	mustOK(t, err)
	ordinary := newAuthLab(t)
	_, err = ordinary.s.importBundle(&bundle)
	mustOK(t, err)
	imported, err := ordinary.s.readSnapshot(snapshot.SnapshotID)
	mustOK(t, err)
	if len(imported.Entries) != 1 || imported.Entries[0].Key != "target" {
		t.Fatal("legacy bundle lost the translated snapshot entry")
	}
	entry := imported.Entries[0]
	digest, err := decodeHexSHA256(entry.ManifestSHA256)
	mustOK(t, err)
	manifest, err := ordinary.s.readVerifiedManifest(entry.ManifestUUID, digest)
	mustOK(t, err)
	if got := repReadAll(t, ordinary.s, manifest, byteRange{0, manifest.TotalLength - 1}); !bytes.Equal(got, two) {
		t.Fatal("legacy bundle changed translated bytes")
	}
	ordinary.verifyOK("legacy translated snapshot bundle")
	mustOK(t, l.s.deleteSnapshot(snapshot.SnapshotID))
	_, err = l.s.AuthorityRevertAll()
	mustOK(t, err)
	if slices.Contains(l.s.formatSnapshot().Features, deflateTranslateFeature) {
		t.Fatal("translation format fence remained after revert")
	}
	l.close()
	l.open()
	l.exact("translated body reverted to ordinary bytes")
	l.verifyOK("translated body reverted to ordinary bytes")
}

func TestGF7SignedAuthorityPanel(t *testing.T) {
	root, families := gf0Corpus(t)
	for _, fam := range families {
		if fam.Name != "typescript-gzip" && fam.Name != "typescript-gzip-adjacent" && fam.Name != "urllib3-gzip" {
			continue
		}
		t.Run(fam.Name, func(t *testing.T) {
			base, _ := gf0ReadFixture(t, root, fam, 0)
			target, _ := gf0ReadFixture(t, root, fam, 1)
			for _, lane := range []string{"archive", "translate"} {
				t.Run(lane, func(t *testing.T) {
					l := newAuthLab(t)
					l.s.writeBinaryManifests = true
					l.put("base", base)
					l.put("target", target)
					_, err := l.s.finishAuthorityArchive(t.Context(), 0)
					mustOK(t, err)
					mustOK(t, l.s.ensureFeature(agFeature))
					c, now := fb13CPU(), time.Now()
					var stats gf7Stats
					if lane == "translate" {
						stats = gf7Publish(t, l, base, target, l.man("base"), l.man("target"))
					} else {
						_, err = l.s.AuthorityOptimize(t.Context(), OptimizePolicy{
							AuthPolicy: AuthPolicy{Apply: true, Order: "oldest", AllowDelta: true, AllowRep: true, CodecReplay: true, MaxDepth: 3, HardCodecs: true},
							Profile:    "archive", Canonical: true, Splice: true, Executables: true, SparseFiles: true, FastReencode: true,
						})
						mustOK(t, err)
					}
					build, wall := int64(fb13CPU()-c), time.Since(now).Nanoseconds()
					_, err = l.s.finishAuthorityArchive(t.Context(), 0)
					mustOK(t, err)
					l.close()
					total, pack, _ := fb13Allocation(t, l.dir)
					l.open()
					get := gf2SignedReader(t, l)
					var whole, small []int64
					for i := range 8 {
						now = time.Now()
						raw := get("target", 0, 0)
						whole = append(whole, time.Since(now).Nanoseconds())
						if !bytes.Equal(raw, target) {
							t.Fatal("signed translated whole differs")
						}
						for j := range 6 {
							off := (len(target) - 4096) * (i*6 + j) / 47
							now = time.Now()
							raw = get("target", off, 4096)
							small = append(small, time.Since(now).Nanoseconds())
							if !bytes.Equal(raw, target[off:off+4096]) {
								t.Fatal("signed translated range differs")
							}
						}
					}
					l.verifyOK("native signed translation")
					row, err := json.Marshal(map[string]any{"family": fam.Name, "lane": lane, "allocated": total, "pack": pack,
						"plan_cpu_ns": build, "plan_wall_ns": wall, "whole_ns": gf0Percentiles(whole), "range_ns": gf0Percentiles(small), "stats": stats})
					mustOK(t, err)
					fmt.Printf("GF7AUTH %s\n", row)
				})
			}
		})
	}
}

func gf7CrashChild(t *testing.T, dir string) {
	s, err := OpenStore(dir)
	mustOK(t, err)
	defer s.Close()
	l := &authLab{t: t, s: s, dir: dir}
	base, target := l.man("base"), l.man("target")
	b := l.read("base", byteRange{0, base.TotalLength - 1})
	v := l.read("target", byteRange{0, target.TotalLength - 1})
	gf7Publish(t, l, b, v, base, target)
}

func TestGF7PublicationCuts(t *testing.T) {
	raw := gf2TarFixture(t, false)
	src := newAuthLab(t)
	src.put("base", gf2GzipFixture(t, raw, 1, 65<<10, "source.tar"))
	src.put("target", gf2GzipFixture(t, raw, 9, 83<<10, "changed.tar"))
	mustOK(t, src.s.ensureFeature(agFeature))
	src.close()
	for _, point := range []string{hookCASAfterFlush, hookAuthAfterFormat, hookAuthPublished} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			mustOK(t, os.CopyFS(dir, os.DirFS(src.dir)))
			if err := os.Remove(filepath.Join(dir, "LOCK")); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if !runKilled(t, "gf7-translate", dir, point, 1) {
				t.Fatal("publication fence was not reached")
			}
			l := &authLab{t: t, dir: dir, want: src.want}
			l.open()
			defer l.close()
			l.exact("translation process cut")
			rec, err := l.s.authLoad(sha256.Sum256(l.want["target"]))
			mustOK(t, err)
			if (rec != nil) != (point == hookAuthPublished) {
				t.Fatal("unexpected translated publication boundary")
			}
			if rec == nil {
				gf7Publish(t, l, l.want["base"], l.want["target"], l.man("base"), l.man("target"))
			}
			_, err = l.s.finishAuthorityArchive(t.Context(), 0)
			mustOK(t, err)
			l.exact("translated publication retry")
			l.verifyOK("translated publication retry")
		})
	}
}

func FuzzGF7TranslatedFrame(f *testing.F) {
	// A fixed block containing five eight-bit literals is exactly 50 bits.
	f.Add([]byte{10, 1, 1, 1, 1, 1}, uint16(49), false)
	f.Add([]byte{2, 12}, uint16(15), false)
	f.Add([]byte{3, 0, 0, 7}, uint16(15), true)
	f.Fuzz(func(t *testing.T, wire []byte, bitLength uint16, corrected bool) {
		if len(wire) > 4096 {
			t.Skip()
		}
		end := int(bitLength)%32768 + 1
		header := repIRBlockMeta{Kind: repDFFixed, NTokens: 5, Final: true}
		repDFinitFixed()
		tables := []gf7SourceBlock{{repDFfixedLitL, repDFfixedDstL}}
		load := func(start, n int) ([]byte, error) {
			if start < 0 || n < 1 || start+n > 256 {
				return nil, errDeflateTranslate
			}
			return make([]byte, n), nil
		}
		got, err := translateReplayFrame(wire, gf7Frame{Start: 0, End: end, Header: header}, tables, load, corrected)
		if err == nil && len(got) != (end+7)/8 {
			t.Fatal("unexpected translated frame length")
		}
	})
}
