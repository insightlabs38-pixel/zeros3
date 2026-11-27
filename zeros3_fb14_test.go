package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"
)

func fb14Input(t *testing.T) []fb13Series {
	t.Helper()
	p := os.Getenv("ZEROS3_FB14_SERIES")
	if p == "" {
		t.Skip("set ZEROS3_FB14_SERIES to the fixture manifest")
	}
	data, err := os.ReadFile(p)
	mustOK(t, err)
	var input []fb13Series
	mustOK(t, json.Unmarshal(data, &input))
	return input
}

func fb14Read(t *testing.T, family fb13Series) [][]byte {
	t.Helper()
	if len(family.Paths) == 0 {
		t.Fatal("empty revision family")
	}
	rows := make([][]byte, 0, len(family.Paths))
	for i, path := range family.Paths {
		raw, err := os.ReadFile(path)
		mustOK(t, err)
		t.Logf("version=%d logical=%d sha256=%x", i, len(raw), sha256.Sum256(raw))
		rows = append(rows, raw)
	}
	return rows
}

func fb14Quantile(samples []int64, percent int) int64 {
	if len(samples) == 0 {
		return 0
	}
	slices.Sort(samples)
	return samples[min((len(samples)*percent+99)/100-1, len(samples)-1)]
}

func TestFB14NativeOptimizedBaselines(t *testing.T) {
	for _, family := range fb14Input(t) {
		t.Run(family.Name, func(t *testing.T) {
			versions := fb14Read(t, family)
			for _, lane := range []string{"native-cdc", "native-preset", "native-archive", "native-mixed"} {
				l := newAuthLab(t)
				l.s.writeBinaryManifests = true
				expected := map[string][]byte{}
				for _, v := range versions {
					l.put("series", v)
					expected[l.man("series").ManifestUUID] = v
				}
				l.offline(func() {
					rep := repackOptions{TargetBytes: defaultPackTargetBytes,
						MaxLivePercent: 100, Compress: true, Recompress: true, Layout: layoutLocality}
					if lane == "native-preset" {
						rep.Prefix = versions[0][:min(32<<10, len(versions[0]))]
					}
					_, err := repackStore(l.dir, rep)
					mustOK(t, err)
				})
				archive := lane == "native-archive" || lane == "native-mixed"
				start := fb13CPU()
				if archive {
					mustOK(t, l.s.ensureFeature(agFeature))
					_, err := l.s.AuthorityOptimize(context.Background(), OptimizePolicy{
						AuthPolicy: AuthPolicy{Apply: true, Order: "oldest", AllowDelta: true,
							AllowRep: true, CodecReplay: true, HardCodecs: true, MaxDepth: 3},
						Profile: lane[7:], Canonical: true, Executables: true, Splice: true,
						SparseFiles: true, FastReencode: true,
					})
					mustOK(t, err)
				}
				optCPU := fb13CPU() - start
				for _, keepLast := range []int{len(versions), 4} {
					if keepLast == 4 && len(versions) <= 5 {
						continue
					}
					pruned, err := pruneHistoryStoreOpen(l.s, historyPruneOptions{Bucket: "b", Key: "series", KeepLast: &keepLast})
					mustOK(t, err)
					start = fb13CPU()
					if archive && pruned.VersionsPruned > 0 {
						_, err := l.s.authorityRebase(context.Background(), "b", "series", true, 0, 0)
						mustOK(t, err)
					}
					if archive {
						_, err := l.s.finishAuthorityArchive(context.Background(), 0)
						mustOK(t, err)
					} else {
						l.reclaim()
					}
					l.close()
					l.open()
					reclaimCPU := fb13CPU() - start
					history := map[string][]byte{"": versions[len(versions)-1]}
					for _, row := range historyFor(t, l.s, "b", "series") {
						history[row.versionID] = expected[row.manifestUUID]
					}
					fb13CheckVersions(t, l, history)
					var ranges, fetched []int64
					man := l.man("series")
					want := versions[len(versions)-1]
					for i := range 48 {
						n := min(4096, len(want))
						off := int64(i*196973) % int64(max(1, len(want)-n+1))
						start := time.Now()
						if got := l.read("series", byteRange{off, off + int64(n) - 1}); !bytes.Equal(got, want[off:off+int64(n)]) {
							t.Fatal("native range mismatch")
						}
						ranges = append(ranges, time.Since(start).Nanoseconds())
						cas, ok, err := l.s.AuthorityReadCost(man, off, int64(n))
						mustOK(t, err)
						if !ok {
							var at int64
							for _, c := range man.Chunks {
								if at < off+int64(n) && at+c.Length > off {
									cas += c.Length
								}
								at += c.Length
							}
						}
						fetched = append(fetched, cas)
					}
					d := fb13DependencyBytes(t, l.s)
					total, packs, _ := fb13Allocation(t, l.dir)
					t.Logf("lane=%s retained_versions=%d total=%d packs=%d dependency=%d p95_range_ns=%d p95_decode_bytes=%d optimize_cpu=%s reclaim_cpu=%s",
						lane, len(history), total, packs, d.ChunkBytes+d.BodyBytes, fb14Quantile(ranges, 95), fb14Quantile(fetched, 95), optCPU, reclaimCPU)
				}
			}
		})
	}
}

const fb14DeltaCodec = byte(0xfe)

// One chunk is one independently checked frame. The pack records target
// identity and length. The patch carries the base identity and stream mode.
func fb14Patch(sum [32]byte, ops []dwDcoreOp, comp *packCompressor) []byte {
	var prog []byte
	for _, op := range ops {
		if op.Base >= 0 {
			prog = append(prog, 1, 0) // one base in the native XD program convention
			prog = binary.AppendUvarint(prog, uint64(op.Off))
			prog = binary.AppendUvarint(prog, uint64(op.N))
		} else {
			prog = append(prog, 0)
			prog = binary.AppendUvarint(prog, uint64(len(op.Lit)))
			prog = append(prog, op.Lit...)
		}
	}
	payload, mode := comp.encode(prog)
	return append(append(bytes.Clone(sum[:]), mode), payload...)
}

func fb14Decode(sum [32]byte, n int, patch, base []byte) ([]byte, error) {
	if len(patch) < 34 || n <= 0 || n > cdcMaxChunkSize || len(base) == 0 || len(base) > cdcMaxChunkSize ||
		(len(patch) > cdcMaxChunkSize+33) {
		return nil, errDelta
	}
	baseSHA := sha256.Sum256(base)
	if !bytes.Equal(patch[:32], baseSHA[:]) {
		return nil, errAuthMismatch
	}
	mode := patch[32]
	if mode != packCodecRaw && mode != packCodecDeflate {
		return nil, errDelta
	}
	var h xdHeader
	h.bases = []xdBaseRef{{baseSHA, int64(len(base))}}
	h.frames = []xdFrame{{span: int64(n), mode: mode, plen: int64(len(patch) - 33)}}
	ops, err := xdDecodeFrame(&h, 0, patch[33:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, n)
	for _, op := range ops {
		if op.base == -1 {
			out = append(out, op.lit...)
		} else {
			out = append(out, base[op.off:op.off+op.n]...)
		}
	}
	if len(out) != n || sha256.Sum256(out) != sum {
		return nil, errAuthMismatch
	}
	return out, nil
}

func TestFB14PatchBounds(t *testing.T) {
	base := genRandomBytes(6323, 64<<10)
	mod := bytes.Clone(base)
	copy(mod[3<<10:], []byte("a changed span"))
	index, err := dwDcoreBuildIndex([][]byte{base}, dwDcoreDefaultParams())
	mustOK(t, err)
	patch := fb14Patch(sha256.Sum256(base), index.Match(mod), newCompressor(true))
	if got, err := fb14Decode(sha256.Sum256(mod), len(mod), patch, base); err != nil || !bytes.Equal(got, mod) {
		t.Fatalf("exact patch: %v", err)
	}
	for i := range len(patch) {
		if _, err := fb14Decode(sha256.Sum256(mod), len(mod), patch[:i], base); err == nil {
			t.Fatalf("accepted prefix of length %d", i)
		}
	}
	other := bytes.Clone(base)
	other[0]++
	if _, err := fb14Decode(sha256.Sum256(mod), len(mod), patch, other); err == nil {
		t.Fatal("accepted foreign base")
	}
}

func TestFB14WindowedMigrationControl(t *testing.T) {
	for _, family := range fb14Input(t) {
		if family.Name != "windowed" {
			continue
		}
		versions := fb14Read(t, family)
		l := newAuthLab(t)
		l.s.writeBinaryManifests = true
		l.put("series", versions[0])
		l.put("series", versions[1])
		m := l.man("series")
		r := l.migrate(AuthPolicy{AllowDelta: true, MaxBases: 1, MaxDepth: 1, MinSaving: .001, MaxReadAmp: -1})
		for _, p := range r.Plans {
			t.Logf("form=%s cdc=%d added=%d reason=%q", p.Form, p.CDCBytes, p.AddedBytes, p.Reason)
		}
		_, err := l.s.finishAuthorityArchive(context.Background(), 0)
		mustOK(t, err)
		for _, row := range historyFor(t, l.s, "b", "series") {
			fb13CheckVersions(t, l, map[string][]byte{row.versionID: versions[0], "": versions[1]})
		}
		var p95 []int64
		for i := range 48 {
			off := int64(i*196973) % (m.TotalLength - 4095)
			n, handled, err := l.s.AuthorityReadCost(m, off, 4096)
			mustOK(t, err)
			if handled {
				p95 = append(p95, n)
			}
		}
		total, packs, _ := fb13Allocation(t, l.dir)
		t.Logf("lane=native-migrate retained_versions=2 total=%d packs=%d p95_decode_bytes=%d", total, packs, fb14Quantile(p95, 95))
	}
}
